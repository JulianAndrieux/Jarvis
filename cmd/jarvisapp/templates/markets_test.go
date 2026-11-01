package templates

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/polymarket"
)

func TestPct(t *testing.T) {
	for in, want := range map[float64]string{0.5: "50,0 %", 0.0235: "2,4 %", 0.003: "0,30 %", 0.9975: "99,75 %", 1: "100,00 %"} {
		if got := Pct(in); got != want {
			t.Errorf("Pct(%v) = %q, veut %q", in, got, want)
		}
	}
}

func TestChange(t *testing.T) {
	for in, want := range map[float64]string{-0.0045: "−0,5 pt", 0.12: "+12,0 pt", 0.0001: "= 0,0 pt"} {
		if got := Change(in); got != want {
			t.Errorf("Change(%v) = %q, veut %q", in, got, want)
		}
	}
}

func TestUSD(t *testing.T) {
	for in, want := range map[float64]string{16786286.5: "16,8 M $", 327952.6: "328,0 k $", 950: "950 $", 2.1e9: "2,1 Md $"} {
		if got := USD(in); got != want {
			t.Errorf("USD(%v) = %q, veut %q", in, got, want)
		}
	}
}

func TestPriceChart(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 11, 1, h, 0, 0, 0, time.UTC) }
	pts := []polymarket.PricePoint{{At: at(0), Price: 0.02}, {At: at(1), Price: 0.04}, {At: at(2), Price: 0.03}}
	c := PriceChart(pts, 600, 160)
	if !strings.HasPrefix(c.Path, "M0.0,") || strings.Count(c.Path, "L") != 2 {
		t.Fatalf("path = %q", c.Path)
	}
	// Dernier point à droite du cadre.
	if !strings.Contains(c.Path, "L600.0,") {
		t.Errorf("le dernier point doit être au bord droit : %q", c.Path)
	}
	// Échelle suivant les données : le plus haut point (0.04) est près du
	// haut, le plus bas (0.02) près du bas — pas une ligne plate en bas
	// d'une échelle 0-100 %.
	if !strings.Contains(c.Path, "L300.0,") {
		t.Fatalf("path = %q", c.Path)
	}
	for _, p := range strings.Split(strings.TrimPrefix(c.Path, "M"), " L") {
		var x, y float64
		if _, err := fmt.Sscanf(p, "%f,%f", &x, &y); err != nil {
			t.Fatal(err)
		}
		if y < 0 || y > 160 {
			t.Errorf("point hors cadre : %q", p)
		}
	}
	if len(c.Ticks) != 3 || c.Ticks[0].Y >= c.Ticks[2].Y {
		t.Errorf("ticks = %+v", c.Ticks)
	}
	if PriceChart(pts[:1], 600, 160).Path != "" {
		t.Error("un seul point : pas de courbe")
	}
}
