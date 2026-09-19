package parsing

import (
	"reflect"
	"testing"

	"github.com/JulianAndrieux/Jarvis/internal/triage"
)

func TestPagesNeedingParsing_ReturnsOnlyUnusablePages(t *testing.T) {
	result := triage.Result{
		Pages: []triage.PageResult{
			{Page: 1, Usable: true},
			{Page: 2, Usable: false},
			{Page: 3, Usable: false},
			{Page: 4, Usable: true},
		},
	}

	got := PagesNeedingParsing(result)
	want := []int{2, 3}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("PagesNeedingParsing() = %v, want %v", got, want)
	}
}

func TestPagesNeedingParsing_AllUsable_ReturnsEmpty(t *testing.T) {
	result := triage.Result{
		Pages: []triage.PageResult{
			{Page: 1, Usable: true},
			{Page: 2, Usable: true},
		},
	}

	got := PagesNeedingParsing(result)

	if len(got) != 0 {
		t.Errorf("PagesNeedingParsing() = %v, want empty", got)
	}
}

func TestPagesNeedingParsing_NoPages_ReturnsEmpty(t *testing.T) {
	got := PagesNeedingParsing(triage.Result{})

	if len(got) != 0 {
		t.Errorf("PagesNeedingParsing() = %v, want empty", got)
	}
}
