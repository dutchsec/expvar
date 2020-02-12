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
