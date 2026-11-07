package miniapps

import (
	"bytes"
	"slices"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Small helpers for cutting pieces out of the captured pages and filling
// them in, the job greengram-web's miniapp-html.js did with regexes.

func parsePart(src string) (*html.Node, error) {
	body := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := html.ParseFragment(strings.NewReader(src), body)
	if err != nil {
		return nil, err
	}
	for _, n := range nodes {
		body.AppendChild(n)
	}
	return body, nil
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func setAttr(n *html.Node, key, val string) {
	for i, a := range n.Attr {
		if a.Key == key {
			n.Attr[i].Val = val
			return
		}
	}
	n.Attr = append(n.Attr, html.Attribute{Key: key, Val: val})
}

func hasClass(n *html.Node, c string) bool {
	return n.Type == html.ElementNode && slices.Contains(strings.Fields(attr(n, "class")), c)
}

func tagIs(name string) func(*html.Node) bool {
	return func(n *html.Node) bool { return n.Type == html.ElementNode && n.Data == name }
}

func classIs(c string) func(*html.Node) bool {
	return func(n *html.Node) bool { return hasClass(n, c) }
}

// all lists matching nodes in document order, not looking inside a match.
func all(root *html.Node, match func(*html.Node) bool) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if match(c) {
				out = append(out, c)
				continue
			}
			walk(c)
		}
	}
	walk(root)
	return out
}

func first(root *html.Node, match func(*html.Node) bool) *html.Node {
	if l := all(root, match); len(l) > 0 {
		return l[0]
	}
	return nil
}

func detach(n *html.Node) {
	if n != nil && n.Parent != nil {
		n.Parent.RemoveChild(n)
	}
}

func removeAll(root *html.Node, match func(*html.Node) bool) {
	for _, n := range all(root, match) {
		detach(n)
	}
}

// setInner replaces n's children with src, parsed in n's context.
func setInner(n *html.Node, src string) {
	for n.FirstChild != nil {
		n.RemoveChild(n.FirstChild)
	}
	nodes, err := html.ParseFragment(strings.NewReader(src), n)
	if err != nil {
		return
	}
	for _, c := range nodes {
		n.AppendChild(c)
	}
}

func setText(n *html.Node, text string) {
	setInner(n, html.EscapeString(text))
}

// replace puts src where n is.
func replace(n *html.Node, src string) {
	if n.Parent == nil {
		return
	}
	nodes, err := html.ParseFragment(strings.NewReader(src), n.Parent)
	if err == nil {
		for _, c := range nodes {
			n.Parent.InsertBefore(c, n)
		}
	}
	n.Parent.RemoveChild(n)
}

func outer(n *html.Node) string {
	var b bytes.Buffer
	html.Render(&b, n)
	return b.String()
}

func inner(n *html.Node) string {
	var b bytes.Buffer
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		html.Render(&b, c)
	}
	return b.String()
}

func clone(n *html.Node) *html.Node {
	c := &html.Node{Type: n.Type, Data: n.Data, DataAtom: n.DataAtom, Namespace: n.Namespace, Attr: slices.Clone(n.Attr)}
	for k := n.FirstChild; k != nil; k = k.NextSibling {
		c.AppendChild(clone(k))
	}
	return c
}
