package admin

import (
    "reflect"
    "testing"
)

func TestMergeSupply(t *testing.T) {
    cases := []struct {
        name     string
        existing any
        want     []string
    }{
        {"no existing entry", nil, []string{"fusion-mlx"}},
        {"non-list existing", "cloud-a", []string{"fusion-mlx"}},
        {"existing preserved as fallback",
            []interface{}{"cloud-a", "cloud-b"},
            []string{"fusion-mlx", "cloud-a", "cloud-b"}},
        {"fusion-mlx not duplicated",
            []interface{}{"cloud-a", "fusion-mlx"},
            []string{"fusion-mlx", "cloud-a"}},
        {"empty strings dropped",
            []interface{}{"", "cloud-a"},
            []string{"fusion-mlx", "cloud-a"}},
        {"non-string entries dropped",
            []interface{}{42, "cloud-a"},
            []string{"fusion-mlx", "cloud-a"}},
    }
    for _, c := range cases {
        if got := mergeSupply(c.existing, "some-model"); !reflect.DeepEqual(got, c.want) {
            t.Errorf("%s: got %v want %v", c.name, got, c.want)
        }
    }
}
