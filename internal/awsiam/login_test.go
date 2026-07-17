package awsiam

import (
	"reflect"
	"testing"
)

func TestUniqueStrings(t *testing.T) {
	cases := []struct {
		in   []string
		want []string
	}{
		{[]string{"a", "b", "a"}, []string{"a", "b"}},
		{[]string{"x", "x", "x"}, []string{"x"}},
		{[]string{"a", "", "b", ""}, []string{"a", "b"}},
		{[]string{"ssm", "iam"}, []string{"ssm", "iam"}},
		{[]string{"same", "same"}, []string{"same"}},
		{nil, nil},
	}
	for _, c := range cases {
		if got := uniqueStrings(c.in...); !reflect.DeepEqual(got, c.want) {
			t.Errorf("uniqueStrings(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}
