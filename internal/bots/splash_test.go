package bots

import (
	"strings"
	"testing"
)

func TestIconPath(t *testing.T) {
	const head = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 512 512">`
	got, err := IconPath(strings.NewReader(head + `<title>x</title><path d="M 10 20 L 30, -40 , 50 60 Z"/><path d="M1 2l3 4z"/></svg>`))
	if err != nil {
		t.Fatal(err)
	}
	// a comma beside a command letter stops a browser drawing the path
	if want := "10,20L30-40,50,60ZM1,2l3,4"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	for _, bad := range []string{
		head + `<circle r="1"/></svg>`,
		head + `<path transform="scale(2)" d="M1 2z"/></svg>`,
		head + `</svg>`,
		`<svg viewBox="0 0 24 24"><path d="M1 2z"/></svg>`,
		head + `<path d="M1 2z<script>"/></svg>`,
	} {
		if _, err := IconPath(strings.NewReader(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestPackPath(t *testing.T) {
	b := packPath("1.5,-2L")
	if len(b) != 7 {
		t.Fatalf("len %d", len(b))
	}
	for _, c := range b {
		if c < 192 {
			t.Errorf("byte %d is outside the alphabet's range", c)
		}
	}
}
