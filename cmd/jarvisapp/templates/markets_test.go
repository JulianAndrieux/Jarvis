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

// Les annonces se placent sur la courbe à leur heure ; hors de la période
// tracée, pas de repère.
func TestChartXAt(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 11, 1, h, 0, 0, 0, time.UTC) }
	c := PriceChart([]polymarket.PricePoint{{At: at(0), Price: 0.2}, {At: at(10), Price: 0.3}}, 600, 160)
	if x, ok := c.XAt(at(5)); !ok || x != 300 {
		t.Errorf("XAt(milieu) = %v %v", x, ok)
	}
	if _, ok := c.XAt(at(11)); ok {
		t.Error("après la courbe : pas de repère")
	}
	if _, ok := (Chart{}).XAt(at(5)); ok {
		t.Error("sans courbe : pas de repère")
	}
}

func TestSafeLink(t *testing.T) {
	for in, want := range map[string]string{"https://a.example/x": "https://a.example/x", "http://a.example/x": "http://a.example/x", "javascript:alert(1)": "", "/local": "", "": ""} {
		if got := SafeLink(in); got != want {
			t.Errorf("SafeLink(%q) = %q, veut %q", in, got, want)
		}
	}
}

// Le délai de collecte n'a de sens que pour un article vu en direct : un
// vieil article trouvé à la première relève d'un flux n'en affiche pas.
func TestLag(t *testing.T) {
	p := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	for d, want := range map[time.Duration]string{20 * time.Second: "vu aussitôt", 3 * time.Minute: "vu 3 min après", 2 * time.Hour: "vu 2 h après", 4 * 24 * time.Hour: ""} {
		if got := Lag(p, p.Add(d)); got != want {
			t.Errorf("Lag(%s) = %q, veut %q", d, got, want)
		}
	}
}
