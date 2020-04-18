package expvar

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Var is an abstract type for all exported variables.
type Var interface {
	// String returns a valid JSON value for the variable.
	// Types with String methods that do not return valid JSON
	// (such as time.Time) must not be used as a Var.
	String() string
}

// Int is a 64-bit integer variable that satisfies the Var interface.
type Int struct {
	i int64
}

func (v *Int) Value() int64 {
	return atomic.LoadInt64(&v.i)
}

func (v *Int) String() string {
	return strconv.FormatInt(atomic.LoadInt64(&v.i), 10)
}

func (v *Int) Increment() {
	atomic.AddInt64(&v.i, 1)
}

func (v *Int) Decrement() {
	atomic.AddInt64(&v.i, -1)
}

func (v *Int) Add(delta int64) {
	atomic.AddInt64(&v.i, delta)
}

func (v *Int) Set(value int64) {
	atomic.StoreInt64(&v.i, value)
}

func (v *Int) MarshalJSON() ([]byte, error) {
	return json.Marshal(v.Value())
}

// Float is a 64-bit float variable that satisfies the Var interface.
type Float struct {
	f uint64
}

func (v *Float) Value() float64 {
	return math.Float64frombits(atomic.LoadUint64(&v.f))
}

func (v *Float) String() string {
	return strconv.FormatFloat(
		math.Float64frombits(atomic.LoadUint64(&v.f)), 'g', -1, 64)
}

// Add adds delta to v.
func (v *Float) Add(delta float64) {
	for {
		cur := atomic.LoadUint64(&v.f)
		curVal := math.Float64frombits(cur)
		nxtVal := curVal + delta
		nxt := math.Float64bits(nxtVal)
		if atomic.CompareAndSwapUint64(&v.f, cur, nxt) {
			return
		}
	}
}

// Set sets v to value.
func (v *Float) Set(value float64) {
	atomic.StoreUint64(&v.f, math.Float64bits(value))
}

func (v *Float) MarshalJSON() ([]byte, error) {
	return json.Marshal(v.Value())
}

// Map is a string-to-Var map variable that satisfies the Var interface.
type Map struct {
	m      sync.Map // map[string]Var
	keysMu sync.RWMutex
	keys   []string // sorted
}

func (v *Map) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "{")
	first := true
	v.Do(func(kv KeyValue) {
		if !first {
			fmt.Fprintf(&b, ", ")
		}

		val, err := json.Marshal(kv.Value)
		if err != nil {
			return
		}

		fmt.Fprintf(&b, "%q: ", kv.Key)

		b.Write(val)

		first = false
	})
	fmt.Fprintf(&b, "}")
	return b.String()
}

// Init removes all keys from the map.
func (v *Map) Init() *Map {
	v.keysMu.Lock()
	defer v.keysMu.Unlock()
	v.keys = v.keys[:0]
	v.m.Range(func(k, _ interface{}) bool {
		v.m.Delete(k)
		return true
	})
	return v
}

// addKey updates the sorted list of keys in v.keys.
func (v *Map) addKey(key string) {
	v.keysMu.Lock()
	defer v.keysMu.Unlock()
	// Using insertion sort to place key into the already-sorted v.keys.
	if i := sort.SearchStrings(v.keys, key); i >= len(v.keys) {
		v.keys = append(v.keys, key)
	} else if v.keys[i] != key {
		v.keys = append(v.keys, "")
		copy(v.keys[i+1:], v.keys[i:])
		v.keys[i] = key
	}
}

func (v *Map) Get(key string) Var {
	i, _ := v.m.Load(key)
	av, _ := i.(Var)
	return av
}

func (v *Map) Set(key string, av Var) {
	// Before we store the value, check to see whether the key is new. Try a Load
	// before LoadOrStore: LoadOrStore causes the key interface to escape even on
	// the Load path.
	if _, ok := v.m.Load(key); !ok {
		if _, dup := v.m.LoadOrStore(key, av); !dup {
			v.addKey(key)
			return
		}
	}

	v.m.Store(key, av)
}

// Add adds delta to the *Int value stored under the given map key.
func (v *Map) Add(key string, delta int64) {
	i, ok := v.m.Load(key)
	if !ok {
		var dup bool
		i, dup = v.m.LoadOrStore(key, new(Int))
		if !dup {
			v.addKey(key)
		}
	}

	// Add to Int; ignore otherwise.
	if iv, ok := i.(*Int); ok {
		iv.Add(delta)
	}
}

// AddFloat adds delta to the *Float value stored under the given map key.
func (v *Map) AddFloat(key string, delta float64) {
	i, ok := v.m.Load(key)
	if !ok {
		var dup bool
		i, dup = v.m.LoadOrStore(key, new(Float))
		if !dup {
			v.addKey(key)
		}
	}

	// Add to Float; ignore otherwise.
	if iv, ok := i.(*Float); ok {
		iv.Add(delta)
	}
}

// Delete deletes the given key from the map.
func (v *Map) Delete(key string) {
	v.keysMu.Lock()
	defer v.keysMu.Unlock()
	i := sort.SearchStrings(v.keys, key)
	if i < len(v.keys) && key == v.keys[i] {
		v.keys = append(v.keys[:i], v.keys[i+1:]...)
		v.m.Delete(key)
	}
}

// Do calls f for each entry in the map.
// The map is locked during the iteration,
// but existing entries may be concurrently updated.
func (v *Map) Do(f func(KeyValue)) {
	v.keysMu.RLock()
	defer v.keysMu.RUnlock()
	for _, k := range v.keys {
		i, _ := v.m.Load(k)
		f(KeyValue{k, i.(Var)})
	}
}

// String is a string variable, and satisfies the Var interface.
type String struct {
	s atomic.Value // string
}

func (v *String) Value() string {
	p, _ := v.s.Load().(string)
	return p
}

// String implements the Var interface. To get the unquoted string
// use Value.
func (v *String) String() string {
	s := v.Value()
	b, _ := json.Marshal(s)
	return string(b)
}

func (v *String) Set(value string) {
	v.s.Store(value)
}

func (v *String) MarshalJSON() ([]byte, error) {
	return json.Marshal(v.Value())
}

var Default = &Bucket{}

type Bucket struct {
	vars sync.Map // map[string]Var

	varKeysMu sync.RWMutex
	varKeys   []string // sorted
}

func Publish(name string, v Var) {
	Default.Publish(name, v)
}

// Publish declares a named exported variable. This should be called from a
// package's init function when it creates its Vars. If the name is already
// registered then this will log.Panic.
func (m *Bucket) Publish(name string, v Var) {
	if _, dup := m.vars.LoadOrStore(name, v); dup {
		log.Panicln("Reuse of exported var name:", name)
	}

	m.varKeysMu.Lock()
	defer m.varKeysMu.Unlock()
	m.varKeys = append(m.varKeys, name)
	sort.Strings(m.varKeys)
}

func Get(name string) Var {
	return Default.Get(name)
}

// Get retrieves a named exported variable. It returns nil if the name has
// not been registered.
func (m *Bucket) Get(name string) Var {
	i, _ := m.vars.Load(name)
	v, _ := i.(Var)
	return v
}

func NewMap(name string) *Map {
	return Default.NewMap(name)
}

func (m *Bucket) NewMap(name string) *Map {
	if v := m.Get(name); v != nil {
		return v.(*Map)
	}

	v := new(Map)
	m.Publish(name, v)
	return v
}

func NewString(name string) *String {
	return Default.NewString(name)
}

func (m *Bucket) NewString(name string) *String {
	if v := m.Get(name); v != nil {
		return v.(*String)
	}

	v := new(String)
	m.Publish(name, v)
	return v
}

func NewInt(name string) *Int {
	return Default.NewInt(name)
}

func (m *Bucket) NewInt(name string) *Int {
	if v := m.Get(name); v != nil {
		return v.(*Int)
	}

	v := new(Int)
	m.Publish(name, v)
	return v
}

func NewFloat(name string) *Float {
	return Default.NewFloat(name)
}

func (m *Bucket) NewFloat(name string) *Float {
	v := new(Float)
	if v := m.Get(name); v != nil {
		return v.(*Float)
	}

	m.Publish(name, v)
	return v
}

// KeyValue represents a single entry in a Map.
type KeyValue struct {
	Key   string
	Value Var
}

func Do(f func(KeyValue)) {
	Default.Do(f)
}

// Do calls f for each exported variable.
// The global variable map is locked during the iteration,
// but existing entries may be concurrently updated.
func (m *Bucket) Do(f func(KeyValue)) {
	m.varKeysMu.RLock()
	defer m.varKeysMu.RUnlock()
	for _, k := range m.varKeys {
		val, _ := m.vars.Load(k)
		f(KeyValue{k, val.(Var)})
	}
}

// Func implements Var by calling the function
// and formatting the returned value using JSON.
type Func func() interface{}

func (f Func) Value() interface{} {
	return f()
}

func (f Func) String() string {
	v, _ := json.Marshal(f())
	return string(v)
}

func expvarHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	fmt.Fprintf(w, "{\n")
	first := true
	Do(func(kv KeyValue) {
		if !first {
			fmt.Fprintf(w, ",\n")
		}
		first = false
		fmt.Fprintf(w, "%q: %s", kv.Key, kv.Value)
	})
	fmt.Fprintf(w, "\n}\n")
}

// Handler returns the expvar HTTP Handler.
//
// This is only needed to install the handler in a non-standard location.
func Handler() http.Handler {
	return http.HandlerFunc(expvarHandler)
}

func cmdline() interface{} {
	return os.Args
}

func memstats() interface{} {
	stats := new(runtime.MemStats)
	runtime.ReadMemStats(stats)
	return *stats
}

func init() {
	http.HandleFunc("/debug/vars", expvarHandler)
	Publish("cmdline", Func(cmdline))
	Publish("memstats", Func(memstats))
}
