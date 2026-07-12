/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package helpers

// branding.go renders the Adhar CLI header: the gradient "ADHAR" wordmark with
// its tagline. Colors are truecolor and downsample automatically on 256/16-color
// terminals; the wordmark still renders under NO_COLOR.

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// --- brand palette (matches the SVG gradient #3B82F6 → #8B5CF6) ---

type rgb struct{ r, g, b int }

func (c rgb) hex() string { return fmt.Sprintf("#%02X%02X%02X", c.r, c.g, c.b) }

func mix(a, b rgb, t float64) rgb {
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	f := func(x, y int) int { return int(float64(x) + (float64(y)-float64(x))*t + 0.5) }
	return rgb{f(a.r, b.r), f(a.g, b.g), f(a.b, b.b)}
}

var (
	brandBlue   = rgb{0x3B, 0x82, 0xF6}
	brandPurple = rgb{0x8B, 0x5C, 0xF6}
	taglineGray = lipgloss.Color("#94A3B8")
)

// gradientBlock paints multi-line ASCII art with a left→right brand gradient.
func gradientBlock(art string, from, to rgb) string {
	lines := strings.Split(art, "\n")
	width := 0
	for _, ln := range lines {
		if w := len([]rune(ln)); w > width {
			width = w
		}
	}
	for i, ln := range lines {
		var b strings.Builder
		for j, r := range []rune(ln) {
			if r == ' ' {
				b.WriteByte(' ')
				continue
			}
			t := 0.0
			if width > 1 {
				t = float64(j) / float64(width-1)
			}
			b.WriteString(lipgloss.NewStyle().
				Foreground(lipgloss.Color(mix(from, to, t).hex())).
				Bold(true).Render(string(r)))
		}
		lines[i] = b.String()
	}
	return strings.Join(lines, "\n")
}

// adharLetters holds the 5-row wordmark glyphs in a solid "ANSI Shadow" block
// font (filled █ with a beveled box-drawing shadow) — one body row shorter than
// the classic 6-row form for a more compact header. Rendered with the brand
// gradient this reads as a clean, modern logotype rather than ASCII line-art.
// Every glyph is exactly 8 runes wide so the columns always align.
var adharLetters = map[rune][]string{
	'A': {" █████╗ ", "██╔══██╗", "███████║", "██║  ██║", "╚═╝  ╚═╝"},
	'D': {"██████╗ ", "██╔══██╗", "██║  ██║", "██████╔╝", "╚═════╝ "},
	'H': {"██╗  ██╗", "███████║", "██╔══██║", "██║  ██║", "╚═╝  ╚═╝"},
	'R': {"██████╗ ", "██╔══██╗", "██████╔╝", "██╔══██╗", "╚═╝  ╚═╝"},
}

// wordmark builds the "ADHAR" block art with `gap` spaces between letters. Each
// letter's rows are padded to its own width so the columns always align.
func wordmark(gap int) string {
	sep := strings.Repeat(" ", gap)
	rows := make([]string, len(adharLetters['A']))
	for r := range rows {
		parts := make([]string, 0, 5)
		for _, c := range "ADHAR" {
			g := adharLetters[c]
			w := 0
			for _, ln := range g {
				if n := len([]rune(ln)); n > w {
					w = n
				}
			}
			ln := g[r]
			for len([]rune(ln)) < w {
				ln += " "
			}
			parts = append(parts, ln)
		}
		rows[r] = strings.Join(parts, sep)
	}
	return strings.Join(rows, "\n")
}

// spacedTagline letter-spaces each word of s with a single, uniform space between
// glyphs (so the spacing reads evenly) and joins words with wordSep spaces (a
// consistent, slightly wider gap so word boundaries stay legible). A hyphen binds
// tight to both neighbours so a compound like "CLOUD-NATIVE" reads as one term
// (and the tagline sits flush under the wordmark rather than overhanging it).
func spacedTagline(s string, wordSep int) string {
	words := strings.Fields(s)
	for i, w := range words {
		var b strings.Builder
		rs := []rune(w)
		for j, r := range rs {
			if j > 0 && r != '-' && rs[j-1] != '-' {
				b.WriteByte(' ')
			}
			b.WriteRune(r)
		}
		words[i] = b.String()
	}
	return strings.Join(words, strings.Repeat(" ", wordSep))
}

// indentBlock left-pads every line of a multi-line block by n spaces.
func indentBlock(s string, n int) string {
	if n <= 0 {
		return s
	}
	pad := strings.Repeat(" ", n)
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = pad + ln
	}
	return strings.Join(lines, "\n")
}

// wordmarkGap is the number of spaces between the ADHAR block glyphs, and
// taglineWordSep the spaces between tagline words. They are tuned together so the
// wordmark (52 cols at gap 3) and the letter-spaced tagline (53 cols) come out
// the same width, and the tagline sits flush under the logo rather than sticking
// out past it.
const (
	wordmarkGap    = 3
	taglineWordSep = 2
)

// RenderBanner is the signature Adhar header: the gradient block wordmark with a
// letter-spaced tagline beneath it. Both blocks are centered to a common width
// (the wider of the two), and the tagline uses uniform single-space letter
// spacing so it reads evenly across the full width.
func RenderBanner() string {
	art := wordmark(wordmarkGap)
	tag := spacedTagline("OPEN CLOUD-NATIVE FOUNDATION", taglineWordSep)

	artW := 0
	for _, ln := range strings.Split(art, "\n") {
		if w := lipgloss.Width(ln); w > artW {
			artW = w
		}
	}
	tagW := lipgloss.Width(tag)
	width := artW
	if tagW > width {
		width = tagW
	}

	word := gradientBlock(indentBlock(art, (width-artW)/2), brandBlue, brandPurple)
	tagline := strings.Repeat(" ", (width-tagW)/2) +
		lipgloss.NewStyle().Foreground(taglineGray).Render(tag)
	return lipgloss.JoinVertical(lipgloss.Left, word, tagline)
}

// RenderBannerLine is the compact, single-line brand strip used for
// non-interactive output (pipes, redirects, CI) so logs stay clean.
func RenderBannerLine(version string) string {
	name := lipgloss.NewStyle().Foreground(lipgloss.Color(brandBlue.hex())).Bold(true).Render("ADHAR")
	rest := lipgloss.NewStyle().Foreground(taglineGray).
		Render(" — Open Cloud-Native Foundation · v" + version)
	return name + rest
}
