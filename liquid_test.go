package liquid

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsTemplateError(t *testing.T) {
	_, err := NewEngine().ParseAndRenderString("{{ syntax error }}", emptyBindings)
	fmt.Printf("%T", err)
	require.True(t, IsTemplateError(err))
	_, err = NewEngine().ParseAndRenderString("{% if %}", emptyBindings)
	require.True(t, IsTemplateError(err))
	_, err = NewEngine().ParseAndRenderString("{% unknown_tag %}", emptyBindings)
	require.True(t, IsTemplateError(err))
	_, err = NewEngine().ParseAndRenderString("{% a | unknown_filter %}", emptyBindings)
	require.True(t, IsTemplateError(err))
}