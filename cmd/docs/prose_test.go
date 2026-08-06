package main

import (
	"strings"
	"testing"
)

func TestScanProse(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []string // substrings, one per expected finding, in order
	}{
		{
			name: "em-dash in prose",
			src:  "The plate — the one on the left — engraves first.\n",
			want: []string{"1:11: em-dash"},
		},
		{
			name: "em-dash inside a fence",
			src:  "```\na — b\n```\nclean prose\n",
			want: nil,
		},
		{
			name: "em-dash inside inline code",
			src:  "the string `a — b` is literal\n",
			want: nil,
		},
		{
			name: "em-dash after an unmatched backtick still counts",
			src:  "a stray ` and then — an em-dash\n",
			want: []string{"1:20: em-dash"},
		},
		{
			name: "lexicon hit, case-insensitive, inflected",
			src:  "Robustness matters.\nWe leveraged the harness.\n",
			want: []string{`1:1: "robust"`, `2:4: "leverage"`},
		},
		{
			name: "lexicon word inside inline code is exempt",
			src:  "type `leverage` to reproduce\n",
			want: nil,
		},
		{
			name: "tilde fences toggle too",
			src:  "~~~\nultimately\n~~~\n",
			want: nil,
		},
		{
			name: "meta phrases",
			src:  "It is worth noting that this works.\n",
			want: []string{`"worth noting"`},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := scanProse("f.md", []byte(tc.src))
			if len(got) != len(tc.want) {
				t.Fatalf("findings = %v, want %d of them", got, len(tc.want))
			}
			for i, sub := range tc.want {
				if !strings.Contains(got[i], sub) {
					t.Errorf("finding %d = %q, want substring %q", i, got[i], sub)
				}
			}
		})
	}
}

func TestScanProseRefs(t *testing.T) {
	src := "![The title editor](images/title.png)\n" +
		"![](images/bare.png)\n" +
		"```\n![in a fence](images/skip.png)\n```\n"
	findings, refs := scanProse("docs/x.md", []byte(src))
	if len(refs) != 2 {
		t.Fatalf("refs = %v, want 2", refs)
	}
	if refs[0].alt != "The title editor" || refs[0].path != "images/title.png" || refs[0].line != 1 {
		t.Errorf("ref 0 = %+v", refs[0])
	}
	if refs[1].alt != "" {
		t.Errorf("ref 1 alt = %q, want empty", refs[1].alt)
	}
	if len(findings) != 0 {
		t.Errorf("prose findings = %v, want none (alt policing is the caller's)", findings)
	}
}

func TestBlankInlineCode(t *testing.T) {
	in := "a `b` c `d` e"
	got := blankInlineCode(in)
	want := "a     c     e"
	if got != want {
		t.Errorf("blankInlineCode(%q) = %q, want %q", in, got, want)
	}
	if len(got) != len(in) {
		t.Errorf("length changed: %d != %d", len(got), len(in))
	}
}
