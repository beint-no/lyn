package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCards(t *testing.T) {
	d := site(t)
	if err := buildErr(d); err != nil {
		t.Fatal(err)
	}
	card, err := os.ReadFile(filepath.Join(d, "public/guider/lonn/card.svg"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(card), `viewBox="0 0 1200 630"`) || !strings.Contains(string(card), "font-weight=\"700\"") {
		t.Fatalf("unexpected card %s", card)
	}
	if _, err := os.Stat(filepath.Join(d, "public/om/card.svg")); err == nil {
		t.Fatal("a card nothing renders was written")
	}
	edit(t, d, "content/nb/guider/lonn/index.md", "title: ", "title: Nytt kort ")
	if err := buildErr(d); err != nil {
		t.Fatal(err)
	}
	card, _ = os.ReadFile(filepath.Join(d, "public/guider/lonn/card.svg"))
	if !strings.Contains(string(card), "Nytt kort") {
		t.Fatalf("incremental build kept the old card: %s", card)
	}
	edit(t, d, "layouts/partials/card.html", `<img src="{{ .Card.RelPermalink }}" alt="" width="{{ .Card.Width }}" height="{{ .Card.Height }}" loading="lazy">`, "")
	if err := buildErr(d); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(d, "public/guider/lonn/card.svg")); err == nil {
		t.Fatal("an unused card was not removed")
	}
}

func TestCardCollision(t *testing.T) {
	d := site(t)
	write(t, d, "content/nb/guider/mva/card.svg", `<svg xmlns="http://www.w3.org/2000/svg" width="1" height="1"/>`)
	edit(t, d, "content/nb/guider/mva/index.md", "](mva.svg", "](card.svg")
	err := buildErr(d)
	if err == nil || !strings.Contains(err.Error(), "card.svg collides") {
		t.Fatalf("expected a collision, got %v", err)
	}
}

func TestCardLayout(t *testing.T) {
	long := strings.Repeat("Regnskapsføring for små aksjeselskaper ", 12)
	tl, dl := cardLayout(long, long)
	if len(tl) != cardMaxTitle || !strings.HasSuffix(tl[len(tl)-1], "…") {
		t.Fatalf("title not capped: %q", tl)
	}
	height := len(tl)*cardTitleLH + cardGap + len(dl)*cardDescLH
	if height > cardH-2*cardMarginY || !strings.HasSuffix(dl[len(dl)-1], "…") {
		t.Fatalf("description overflows: %d px, %q", height, dl)
	}
	limit := float64(cardW - 2*cardMarginX)
	for _, l := range tl {
		if cardWidth(l, cardTitlePx, true) > limit {
			t.Fatalf("title line too wide: %q", l)
		}
	}
	for _, l := range dl {
		if cardWidth(l, cardDescPx, false) > limit {
			t.Fatalf("description line too wide: %q", l)
		}
	}
	tl, dl = cardLayout("Kort tittel", "")
	if len(tl) != 1 || dl != nil {
		t.Fatalf("short card: %q %q", tl, dl)
	}
	if svg := string(cardSVG("A & B <c>", "")); !strings.Contains(svg, "A &amp; B &lt;c&gt;") {
		t.Fatalf("card text not escaped: %s", svg)
	}
}
