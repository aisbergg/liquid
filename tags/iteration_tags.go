package tags

import (
	"fmt"
	"io"
	"math"
	"reflect"
	"sort"

	yaml "gopkg.in/yaml.v2"

	"github.com/osteele/liquid/expressions"
	"github.com/osteele/liquid/render"
	"github.com/osteele/liquid/values"
)

// An IterationKeyedMap is a map that yields its keys, instead of (key, value) pairs, when iterated.
type IterationKeyedMap map[string]interface{}

const forloopVarName = "forloop"

var errLoopContinueLoop = fmt.Errorf("continue outside a loop")
var errLoopBreak = fmt.Errorf("break outside a loop")

type iterable interface {
	Len() int
	Index(int) interface{}
}

func breakTag(string) (func(io.Writer, render.Context) error, error) {
	return func(_ io.Writer, ctx render.Context) error {
		return ctx.WrapError(errLoopBreak)
	}, nil
}

func continueTag(string) (func(io.Writer, render.Context) error, error) {
	return func(_ io.Writer, ctx render.Context) error {
		return ctx.WrapError(errLoopContinueLoop)
	}, nil
}

func cycleTag(args string) (func(io.Writer, render.Context) error, error) {
	stmt, err := expressions.ParseStatement(expressions.CycleStatementSelector, args)
	if err != nil {
		return nil, err
	}
	cycle := stmt.Cycle
	return func(w io.Writer, ctx render.Context) error {
		loopVar := ctx.Get(forloopVarName)
		if loopVar == nil {
			return ctx.Errorf("cycle must be within a forloop")
		}
		// The next few lines could panic if the user spoofs us by creating their own loop object.
		// “C++ protects against accident, not against fraud.” – Bjarne Stroustrup
		loopRec := loopVar.(map[string]interface{})
		cycleMap := loopRec[".cycles"].(map[string]int)
		group, values := cycle.Group, cycle.Values
		n := cycleMap[group]
		cycleMap[group] = n + 1
		// The parser guarantees that there will be at least one item.
		_, err = io.WriteString(w, values[n%len(values)])
		return err
	}, nil
}

func loopTagCompiler(node render.BlockNode) (func(io.Writer, render.Context) error, error) {
	stmt, err := expressions.ParseStatement(expressions.LoopStatementSelector, node.Args)
	if err != nil {
		return nil, err
	}
	return loopRenderer{stmt.Loop, node.Name}.render, nil
}

type loopRenderer struct {
	expressions.Loop
	tagName string
}

func (loop loopRenderer) render(w io.Writer, ctx render.Context) error {
	// loop modifiers
	val, err := ctx.Evaluate(loop.Expr)
	if err != nil {
		return err
	}
	iter := makeIterator(val)
	if iter == nil {
		return nil
	}
	iter, err = applyLoopModifiers(loop.Loop, ctx, iter)
	if err != nil {
		return err
	}

	// loop decorator
	decorator, err := makeLoopDecorator(loop, ctx)
	if err != nil {
		return err
	}

	// shallow-bind the loop variables; restore on exit
	defer func(index, forloop interface{}) {
		ctx.Set(forloopVarName, index)
		ctx.Set(loop.Variable, forloop)
	}(ctx.Get(forloopVarName), ctx.Get(loop.Variable))
	cycleMap := map[string]int{}
loop:
	for i, len := 0, iter.Len(); i < len; i++ {
		ctx.Set(loop.Variable, iter.Index(i))
		ctx.Set(forloopVarName, map[string]interface{}{
			"first":   i == 0,
			"last":    i == len-1,
			"index":   i + 1,
			"index0":  i,
			"rindex":  len - i,
			"rindex0": len - i - 1,
			"length":  len,
			".cycles": cycleMap,
		})
		decorator.before(w, i)
		err := ctx.RenderChildren(w)
		decorator.after(w, i, len)
		switch {
		case err == nil:
		// fall through
		case err.Cause() == errLoopBreak:
			break loop
		case err.Cause() == errLoopContinueLoop:
			continue loop
		default:
			return err
		}
	}
	return nil
}

func makeLoopDecorator(loop loopRenderer, ctx render.Context) (loopDecorator, error) {
	if loop.tagName == "tablerow" {
		if loop.Cols != nil {
			val, err := ctx.Evaluate(loop.Cols)
			if err != nil {
				return nil, err
			}
			cols, ok := val.(int)
			if !ok {
				return nil, ctx.Errorf("loop cols must be an integer")
			}
			if cols > 0 {
				return tableRowDecorator(cols), nil
			}
		}
		return tableRowDecorator(math.MaxInt32), nil
	}
	return forLoopDecorator{}, nil
}

type loopDecorator interface {
	before(io.Writer, int)
	after(io.Writer, int, int)
}

type forLoopDecorator struct{}

func (d forLoopDecorator) before(io.Writer, int)     {}
func (d forLoopDecorator) after(io.Writer, int, int) {}

type tableRowDecorator int

func (c tableRowDecorator) before(w io.Writer, i int) {
	cols := int(c)
	row, col := i/cols, i%cols
	if col == 0 {
		if _, err := fmt.Fprintf(w, `<tr class="row%d">`, row+1); err != nil {
			panic(err)
		}
	}
	if _, err := fmt.Fprintf(w, `<td class="col%d">`, col+1); err != nil {
		panic(err)
	}
}

func (c tableRowDecorator) after(w io.Writer, i, len int) {
	cols := int(c)
	if _, err := io.WriteString(w, `</td>`); err != nil {
		panic(err)
	}
	if (i+1)%cols == 0 || i+1 == len {
		if _, err := io.WriteString(w, `</tr>`); err != nil {
			panic(err)
		}
	}
}

func applyLoopModifiers(loop expressions.Loop, ctx render.Context, iter iterable) (iterable, error) {
	if loop.Reversed {
		iter = reverseWrapper{iter}
	}

	if loop.Offset != nil {
		val, err := ctx.Evaluate(loop.Offset)
		if err != nil {
			return nil, err
		}
		offset, ok := val.(int)
		if !ok {
			return nil, ctx.Errorf("loop offset must be an integer")
		}
		if offset > 0 {
			iter = offsetWrapper{iter, offset}
		}
	}

	if loop.Limit != nil {
		val, err := ctx.Evaluate(loop.Limit)
		if err != nil {
			return nil, err
		}
		limit, ok := val.(int)
		if !ok {
			return nil, ctx.Errorf("loop limit must be an integer")
		}
		if limit >= 0 {
			iter = limitWrapper{iter, limit}
		}
	}

	return iter, nil
}

func makeIterator(value interface{}) iterable {
	if iter, ok := value.(iterable); ok {
		return iter
	}
	if value == nil {
		return nil
	}
	switch value := value.(type) {
	case IterationKeyedMap:
		return makeIterationKeyedMap(value)
	case yaml.MapSlice:
		return mapSliceWrapper{value}
	}
	if om, ok := value.(values.Orderedmapper); ok {
		if reflect.ValueOf(om).IsNil() {
			return nil
		}
		array := make([][]interface{}, 0, om.Len())
		fn := func(key, value interface{}) bool {
			array = append(array, []interface{}{key, value})
			return true
		}
		om.Range(fn)
		return sliceWrapper(reflect.ValueOf(array))
	}
	switch reflect.TypeOf(value).Kind() {
	case reflect.Array, reflect.Slice:
		return sliceWrapper(reflect.ValueOf(value))
	case reflect.Map:
		rv := reflect.ValueOf(value)
		array := make([][]interface{}, rv.Len())
		for i, k := range rv.MapKeys() {
			v := rv.MapIndex(k)
			array[i] = []interface{}{k.Interface(), v.Interface()}
		}
		return sliceWrapper(reflect.ValueOf(array))
	default:
		return nil
	}
}

func makeIterationKeyedMap(m map[string]interface{}) iterable {
	// Iteration chooses a random start, so we need a copy of the keys to iterate through them.
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Sorting isn't necessary to match Shopify liquid, but it simplifies debugging.
	sort.Strings(keys)
	return sliceWrapper(reflect.ValueOf(keys))
}

type sliceWrapper reflect.Value

func (w sliceWrapper) Len() int                { return reflect.Value(w).Len() }
func (w sliceWrapper) Index(i int) interface{} { return reflect.Value(w).Index(i).Interface() }

type mapSliceWrapper struct{ ms yaml.MapSlice }

func (w mapSliceWrapper) Len() int { return len(w.ms) }
func (w mapSliceWrapper) Index(i int) interface{} {
	item := w.ms[i]
	return []interface{}{item.Key, item.Value}
}

type limitWrapper struct {
	i iterable
	n int
}

func (w limitWrapper) Len() int                { return intMin(w.n, w.i.Len()) }
func (w limitWrapper) Index(i int) interface{} { return w.i.Index(i) }

type offsetWrapper struct {
	i iterable
	n int
}

func (w offsetWrapper) Len() int                { return intMax(0, w.i.Len()-w.n) }
func (w offsetWrapper) Index(i int) interface{} { return w.i.Index(i + w.n) }

type reverseWrapper struct {
	i iterable
}

func (w reverseWrapper) Len() int                { return w.i.Len() }
func (w reverseWrapper) Index(i int) interface{} { return w.i.Index(w.i.Len() - 1 - i) }

func intMax(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func intMin(a, b int) int {
	if a < b {
		return a
	}
	return b
}
