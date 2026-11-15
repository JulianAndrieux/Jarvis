package formats

import (
	"errors"
	"testing"
)

func pathWith(names ...string) func(string) (string, error) {
	return func(name string) (string, error) {
		for _, n := range names {
			if n == name {
				return "/usr/bin/" + name, nil
			}
		}
		return "", errors.New("introuvable")
	}
}

func TestImageTool(t *testing.T) {
	for _, tc := range []struct {
		name       string
		c          *LocalConverter
		path       []string
		wantBin    string
		wantMagick bool
	}{
		{"macOS : sips", &LocalConverter{}, []string{"sips", "convert"}, "sips", false},
		{"Linux, ImageMagick 6", &LocalConverter{}, []string{"convert"}, "convert", true},
		{"Linux, ImageMagick 7", &LocalConverter{}, []string{"magick", "convert"}, "magick", true},
		{"sips imposé", &LocalConverter{Sips: "/opt/sips"}, []string{"convert"}, "/opt/sips", false},
		{"ImageMagick imposé", &LocalConverter{Magick: "/opt/magick"}, []string{"sips"}, "/opt/magick", true},
		{"rien : sips, pour une erreur qui le nomme", &LocalConverter{}, nil, "sips", false},
	} {
		bin, magick := tc.c.imageTool(pathWith(tc.path...))
		if bin != tc.wantBin || magick != tc.wantMagick {
			t.Errorf("%s: imageTool = %q, %v ; want %q, %v", tc.name, bin, magick, tc.wantBin, tc.wantMagick)
		}
	}
}
