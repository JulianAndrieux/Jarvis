package main

import "testing"

// L'URI MongoDB (mot de passe compris) arrive par l'environnement, pour
// ne pas apparaître dans la ligne de commande (ps). L'option explicite
// reste prioritaire.
func TestMongoURI_FlagThenEnvironment(t *testing.T) {
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == "MONGO_URI" {
				return v
			}
			return ""
		}
	}
	cases := []struct{ flag, env, want string }{
		{"", "mongodb://env", "mongodb://env"},
		{"mongodb://flag", "mongodb://env", "mongodb://flag"},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := mongoURI(c.flag, env(c.env)); got != c.want {
			t.Errorf("mongoURI(%q, env=%q) = %q, want %q", c.flag, c.env, got, c.want)
		}
	}
}
