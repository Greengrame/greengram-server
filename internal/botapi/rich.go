package botapi

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"

	"github.com/iamxvbaba/td/tg"
	"github.com/yuin/goldmark"
	"golang.org/x/net/html"

	"github.com/Greengrame/greengram-server/internal/ids"
	"github.com/Greengrame/greengram-server/internal/media"
)

type richBlockInput struct {
	Type    string            `json:"type"`
	Text    json.RawMessage   `json:"text"`
	Credit  json.RawMessage   `json:"credit"`
	Summary json.RawMessage   `json:"summary"`
	Caption json.RawMessage   `json:"caption"`
	Blocks  []json.RawMessage `json:"blocks"`
	Items   []struct {
		Blocks   []json.RawMessage `json:"blocks"`
		Checkbox bool              `json:"has_checkbox"`
		Checked  bool              `json:"is_checked"`
		Value    int               `json:"value"`
		Type     string            `json:"type"`
	} `json:"items"`
	Cells [][]struct {
		Text    json.RawMessage `json:"text"`
		Header  bool            `json:"is_header"`
		Colspan int             `json:"colspan"`
		Rowspan int             `json:"rowspan"`
		Align   string          `json:"align"`
		Valign  string          `json:"valign"`
	} `json:"cells"`
	Bordered   bool   `json:"is_bordered"`
	Striped    bool   `json:"is_striped"`
	Compact    bool   `json:"is_compact"`
	Open       bool   `json:"is_open"`
	Language   string `json:"language"`
	Name       string `json:"name"`
	Size       int    `json:"size"`
	Expression string `json:"expression"`
}

func parseRichBlocks(raw []json.RawMessage, depth int) ([]tg.PageBlockClass, error) {
	var blocks = []tg.PageBlockClass{}
	var input richBlockInput
	var item json.RawMessage
	var text, credit, summary tg.RichTextClass
	var children []tg.PageBlockClass
	var block tg.PageBlockClass
	var list *tg.PageBlockOrderedList
	var table *tg.PageBlockTable
	var row tg.PageTableRow
	var cell tg.PageTableCell
	var i, j int
	var err error
	if depth > 16 || len(raw) > 500 {
		return nil, bad("rich message is too complex")
	}
	for _, item = range raw {
		input = richBlockInput{}
		err = json.Unmarshal(item, &input)
		if err != nil {
			return nil, bad("invalid rich block")
		}
		text, err = parseRichText(input.Text, 0)
		if err != nil {
			return nil, err
		}
		credit, err = parseRichText(input.Credit, 0)
		if err != nil {
			return nil, err
		}
		children, err = parseRichBlocks(input.Blocks, depth+1)
		if err != nil {
			return nil, err
		}
		switch input.Type {
		case "paragraph":
			block = &tg.PageBlockParagraph{Text: text}
		case "heading":
			switch input.Size {
			case 1:
				block = &tg.PageBlockHeading1{Text: text}
			case 2:
				block = &tg.PageBlockHeading2{Text: text}
			case 3:
				block = &tg.PageBlockHeading3{Text: text}
			case 4:
				block = &tg.PageBlockHeading4{Text: text}
			case 5:
				block = &tg.PageBlockHeading5{Text: text}
			case 6:
				block = &tg.PageBlockHeading6{Text: text}
			default:
				return nil, bad("heading size must be 1-6")
			}
		case "pre":
			block = &tg.PageBlockPreformatted{Text: text, Language: input.Language}
		case "footer":
			block = &tg.PageBlockFooter{Text: text}
		case "divider":
			block = &tg.PageBlockDivider{}
		case "mathematical_expression":
			block = &tg.PageBlockMath{Source: input.Expression}
		case "anchor":
			block = &tg.PageBlockAnchor{Name: input.Name}
		case "blockquote":
			block = &tg.PageBlockBlockquoteBlocks{Blocks: children, Caption: credit}
		case "expandable_blockquote":
			block = &tg.PageBlockBlockquote{Text: text, Caption: credit, Collapsed: true}
		case "pullquote":
			block = &tg.PageBlockPullquote{Text: text, Caption: credit}
		case "details":
			summary, err = parseRichText(input.Summary, 0)
			if err != nil {
				return nil, err
			}
			block = &tg.PageBlockDetails{Title: summary, Blocks: children, Open: input.Open}
		case "list":
			list = &tg.PageBlockOrderedList{Items: []tg.PageListOrderedItemClass{}}
			for i = range input.Items {
				children, err = parseRichBlocks(input.Items[i].Blocks, depth+1)
				if err != nil {
					return nil, err
				}
				if input.Items[i].Type != "" && !strings.Contains("aAiI1", input.Items[i].Type) {
					return nil, bad("invalid list label type")
				}
				list.Items = append(list.Items, &tg.PageListOrderedItemBlocks{Blocks: children, Checkbox: input.Items[i].Checkbox, Checked: input.Items[i].Checked, Value: input.Items[i].Value, Type: input.Items[i].Type})
			}
			block = list
		case "table":
			summary, err = parseRichText(input.Caption, 0)
			if err != nil {
				return nil, err
			}
			table = &tg.PageBlockTable{Title: summary, Bordered: input.Bordered, Striped: input.Striped, Compact: input.Compact, Rows: []tg.PageTableRow{}}
			if len(input.Cells) > 500 {
				return nil, bad("table is too large")
			}
			for i = range input.Cells {
				row = tg.PageTableRow{Cells: []tg.PageTableCell{}}
				if len(input.Cells[i]) > 20 {
					return nil, bad("table is too wide")
				}
				for j = range input.Cells[i] {
					cell = tg.PageTableCell{Header: input.Cells[i][j].Header, Colspan: input.Cells[i][j].Colspan, Rowspan: input.Cells[i][j].Rowspan, AlignCenter: input.Cells[i][j].Align == "center", AlignRight: input.Cells[i][j].Align == "right", ValignMiddle: input.Cells[i][j].Valign == "middle", ValignBottom: input.Cells[i][j].Valign == "bottom"}
					cell.Text, err = parseRichText(input.Cells[i][j].Text, 0)
					if err != nil {
						return nil, err
					}
					row.Cells = append(row.Cells, cell)
				}
				table.Rows = append(table.Rows, row)
			}
			block = table
		default:
			return nil, bad("unsupported rich block type: " + input.Type)
		}
		blocks = append(blocks, block)
	}
	return blocks, nil
}

func (s *Server) readRich(c *call) ([]byte, string, error) {
	var input struct {
		Blocks   []json.RawMessage `json:"blocks"`
		HTML     string            `json:"html"`
		Markdown string            `json:"markdown"`
		RTL      bool              `json:"is_rtl"`
	}
	var blocks []tg.PageBlockClass
	var data bytes.Buffer
	var present bool
	var count int
	var err error
	if s.Media == nil {
		return nil, "", bad("media service is unavailable")
	}
	present, err = c.p.json("rich_message", &input)
	if err != nil {
		return nil, "", err
	}
	if !present {
		return nil, "", bad("rich_message is required")
	}
	if len(input.Blocks) != 0 {
		count++
	}
	if input.HTML != "" {
		count++
	}
	if input.Markdown != "" {
		count++
	}
	if count != 1 {
		return nil, "", bad("specify exactly one rich message format")
	}
	if len(input.HTML)+len(input.Markdown) > 512<<10 {
		return nil, "", bad("rich message is too large")
	}
	if input.Markdown != "" {
		err = goldmark.Convert([]byte(input.Markdown), &data)
		if err != nil {
			return nil, "", bad("invalid rich markdown")
		}
		input.HTML = data.String()
	}
	if input.HTML != "" {
		blocks, err = parseRichHTML(input.HTML)
	} else {
		blocks, err = parseRichBlocks(input.Blocks, 0)
	}
	if err != nil {
		return nil, "", err
	}
	return s.Media.RichFromInput(c.ctx, &tg.InputRichMessage{Blocks: blocks, Rtl: input.RTL})
}

func (s *Server) sendRichMessage(c *call) (any, error) {
	var to chat
	var out Out
	var err error
	to, err = s.messageChat(c)
	if err != nil {
		return nil, err
	}
	err = s.messageWritable(c, to)
	if err != nil {
		return nil, err
	}
	out.Rich, out.Text, err = s.readRich(c)
	if err != nil {
		return nil, err
	}
	out.Markup, _, err = markup(c.p)
	if err != nil {
		return nil, err
	}
	out.ReplyTo, err = replyTo(c.p)
	if err != nil {
		return nil, err
	}
	return s.sendCall(c, to, out)
}

func (v *view) richBody(out *apiMessage, raw []byte) {
	var rich tg.RichMessage
	var ok bool
	rich, ok = media.DecodeRich(raw)
	if !ok {
		return
	}
	out.Text = ""
	out.Entities = nil
	out.RichMessage = map[string]any{"blocks": v.richBlocksJSON(rich.Blocks), "is_rtl": rich.Rtl}
}

func (v *view) richBlocksJSON(blocks []tg.PageBlockClass) []any {
	var output = []any{}
	var block tg.PageBlockClass
	var value reflect.Value
	var field reflect.Value
	var text tg.RichTextClass
	var entry map[string]any
	var name string
	var kind string
	var list *tg.PageBlockOrderedList
	var item tg.PageListOrderedItemClass
	var items []any
	var itemValue *tg.PageListOrderedItemBlocks
	var i int
	for _, block = range blocks {
		value = reflect.ValueOf(block).Elem()
		name = value.Type().Name()
		entry = map[string]any{}
		switch name {
		case "PageBlockParagraph":
			kind = "paragraph"
		case "PageBlockHeader", "PageBlockSubheader":
			kind = "heading"
			entry["size"] = 2
		case "PageBlockHeading1", "PageBlockHeading2", "PageBlockHeading3", "PageBlockHeading4", "PageBlockHeading5", "PageBlockHeading6":
			kind = "heading"
			i, _ = strconv.Atoi(name[len(name)-1:])
			entry["size"] = i
		case "PageBlockPreformatted":
			kind = "pre"
			entry["language"] = block.(*tg.PageBlockPreformatted).Language
		case "PageBlockFooter":
			kind = "footer"
		case "PageBlockDivider":
			kind = "divider"
		case "PageBlockMath":
			kind = "mathematical_expression"
			entry["expression"] = block.(*tg.PageBlockMath).Source
		case "PageBlockAnchor":
			kind = "anchor"
			entry["name"] = block.(*tg.PageBlockAnchor).Name
		case "PageBlockBlockquoteBlocks":
			kind = "blockquote"
			entry["blocks"] = v.richBlocksJSON(block.(*tg.PageBlockBlockquoteBlocks).Blocks)
			entry["credit"] = v.richTextJSON(block.(*tg.PageBlockBlockquoteBlocks).Caption)
		case "PageBlockBlockquote":
			kind = "blockquote"
			if block.(*tg.PageBlockBlockquote).Collapsed {
				kind = "expandable_blockquote"
			}
			entry["credit"] = v.richTextJSON(block.(*tg.PageBlockBlockquote).Caption)
		case "PageBlockPullquote":
			kind = "pullquote"
			entry["credit"] = v.richTextJSON(block.(*tg.PageBlockPullquote).Caption)
		case "PageBlockDetails":
			kind = "details"
			entry["summary"] = v.richTextJSON(block.(*tg.PageBlockDetails).Title)
			entry["blocks"] = v.richBlocksJSON(block.(*tg.PageBlockDetails).Blocks)
			entry["is_open"] = block.(*tg.PageBlockDetails).Open
		case "PageBlockTable":
			kind = "table"
			entry = v.richTableJSON(block.(*tg.PageBlockTable))
		case "PageBlockOrderedList":
			kind = "list"
			list = block.(*tg.PageBlockOrderedList)
			items = []any{}
			for _, item = range list.Items {
				switch item.(type) {
				case *tg.PageListOrderedItemBlocks:
					itemValue = item.(*tg.PageListOrderedItemBlocks)
					items = append(items, map[string]any{"blocks": v.richBlocksJSON(itemValue.Blocks), "has_checkbox": itemValue.Checkbox, "is_checked": itemValue.Checked, "value": itemValue.Value, "type": itemValue.Type})
				case *tg.PageListOrderedItemText:
					items = append(items, map[string]any{"blocks": []any{map[string]any{"type": "paragraph", "text": v.richTextJSON(item.(*tg.PageListOrderedItemText).Text)}}})
				}
			}
			entry["items"] = items
		default:
			kind = "paragraph"
			entry["text"] = ""
		}
		field = value.FieldByName("Text")
		if field.IsValid() {
			text, _ = field.Interface().(tg.RichTextClass)
			entry["text"] = v.richTextJSON(text)
		}
		entry["type"] = kind
		output = append(output, entry)
	}
	return output
}

func parseRichHTML(source string) ([]tg.PageBlockClass, error) {
	var document *html.Node
	var body *html.Node
	var blocks []tg.PageBlockClass
	var err error
	document, err = html.Parse(strings.NewReader(source))
	if err != nil {
		return nil, bad("invalid rich HTML")
	}
	body = findRichHTMLBody(document)
	if body == nil {
		return nil, bad("invalid rich HTML")
	}
	blocks, err = readRichHTMLBlocks(body, 0)
	return blocks, err
}

func findRichHTMLBody(node *html.Node) *html.Node {
	var child *html.Node
	var found *html.Node
	if node.Type == html.ElementNode && node.Data == "body" {
		return node
	}
	for child = node.FirstChild; child != nil; child = child.NextSibling {
		found = findRichHTMLBody(child)
		if found != nil {
			return found
		}
	}
	return nil
}

func readRichHTMLBlocks(parent *html.Node, depth int) ([]tg.PageBlockClass, error) {
	var output = []tg.PageBlockClass{}
	var inline = []tg.RichTextClass{}
	var child *html.Node
	var text tg.RichTextClass
	var blocks []tg.PageBlockClass
	var block tg.PageBlockClass
	var err error
	var isBlock bool
	if depth > 32 {
		return nil, bad("rich HTML is too complex")
	}
	for child = parent.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.CommentNode {
			continue
		}
		isBlock = child.Type == html.ElementNode && strings.Contains("|p|h1|h2|h3|h4|h5|h6|pre|footer|hr|blockquote|aside|details|div|tg-math-block|", "|"+child.Data+"|")
		if !isBlock {
			if child.Type == html.TextNode && strings.TrimSpace(child.Data) == "" && len(inline) == 0 {
				continue
			}
			text, err = readRichHTMLText(child, depth+1)
			if err != nil {
				return nil, err
			}
			inline = append(inline, text)
			continue
		}
		if len(inline) != 0 {
			output = append(output, &tg.PageBlockParagraph{Text: &tg.TextConcat{Texts: inline}})
			inline = nil
		}
		text, err = readRichHTMLChildren(child, depth+1)
		if err != nil {
			return nil, err
		}
		switch child.Data {
		case "h1":
			block = &tg.PageBlockHeading1{Text: text}
		case "h2":
			block = &tg.PageBlockHeading2{Text: text}
		case "h3":
			block = &tg.PageBlockHeading3{Text: text}
		case "h4":
			block = &tg.PageBlockHeading4{Text: text}
		case "h5":
			block = &tg.PageBlockHeading5{Text: text}
		case "h6":
			block = &tg.PageBlockHeading6{Text: text}
		case "pre":
			block = &tg.PageBlockPreformatted{Text: text, Language: strings.TrimPrefix(richHTMLAttribute(child.FirstChild, "class"), "language-")}
		case "footer":
			block = &tg.PageBlockFooter{Text: text}
		case "hr":
			block = &tg.PageBlockDivider{}
		case "blockquote":
			blocks, err = readRichHTMLBlocks(child, depth+1)
			block = &tg.PageBlockBlockquoteBlocks{Blocks: blocks, Caption: &tg.TextEmpty{}}
		case "aside":
			block = &tg.PageBlockPullquote{Text: text, Caption: &tg.TextEmpty{}}
		case "tg-math-block":
			block = &tg.PageBlockMath{Source: richTextPlain(text)}
		default:
			block = &tg.PageBlockParagraph{Text: text}
		}
		if err != nil {
			return nil, err
		}
		output = append(output, block)
	}
	if len(inline) != 0 {
		output = append(output, &tg.PageBlockParagraph{Text: &tg.TextConcat{Texts: inline}})
	}
	return output, nil
}

func richHTMLAttribute(node *html.Node, key string) string {
	var attribute html.Attribute
	if node == nil {
		return ""
	}
	for _, attribute = range node.Attr {
		if attribute.Key == key {
			return attribute.Val
		}
	}
	return ""
}

func readRichHTMLChildren(node *html.Node, depth int) (tg.RichTextClass, error) {
	var output = []tg.RichTextClass{}
	var child *html.Node
	var text tg.RichTextClass
	var err error
	for child = node.FirstChild; child != nil; child = child.NextSibling {
		text, err = readRichHTMLText(child, depth+1)
		if err != nil {
			return nil, err
		}
		output = append(output, text)
	}
	return &tg.TextConcat{Texts: output}, nil
}

func readRichHTMLText(node *html.Node, depth int) (tg.RichTextClass, error) {
	var text tg.RichTextClass
	var err error
	var url string
	var id int64
	if depth > 32 {
		return nil, bad("rich HTML is too complex")
	}
	if node.Type == html.TextNode {
		return &tg.TextPlain{Text: node.Data}, nil
	}
	if node.Type == html.CommentNode {
		return &tg.TextEmpty{}, nil
	}
	text, err = readRichHTMLChildren(node, depth+1)
	if err != nil {
		return nil, err
	}
	switch node.Data {
	case "b", "strong":
		return &tg.TextBold{Text: text}, nil
	case "i", "em":
		return &tg.TextItalic{Text: text}, nil
	case "u", "ins":
		return &tg.TextUnderline{Text: text}, nil
	case "s", "strike", "del":
		return &tg.TextStrike{Text: text}, nil
	case "code":
		return &tg.TextFixed{Text: text}, nil
	case "sub":
		return &tg.TextSubscript{Text: text}, nil
	case "sup":
		return &tg.TextSuperscript{Text: text}, nil
	case "mark":
		return &tg.TextMarked{Text: text}, nil
	case "tg-spoiler":
		return &tg.TextSpoiler{Text: text}, nil
	case "tg-ton":
		return &tg.TextTonAddress{Text: text}, nil
	case "tg-math":
		return &tg.TextMath{Source: richTextPlain(text)}, nil
	case "tg-emoji":
		id, err = strconv.ParseInt(richHTMLAttribute(node, "emoji-id"), 10, 64)
		if err != nil {
			return nil, bad("invalid custom emoji")
		}
		return &tg.TextCustomEmoji{DocumentID: id, Alt: richTextPlain(text)}, nil
	case "br":
		return &tg.TextPlain{Text: "\n"}, nil
	case "a":
		url = richHTMLAttribute(node, "href")
		if strings.HasPrefix(url, "#") {
			return &tg.TextURL{Text: text, URL: url}, nil
		}
		if !validURL(url, "http", "https", "tg", "ton", "mailto", "tel") {
			return nil, bad("invalid rich HTML link")
		}
		if strings.HasPrefix(url, "tg://user?id=") {
			id, err = strconv.ParseInt(strings.TrimPrefix(url, "tg://user?id="), 10, 64)
			if err != nil {
				return nil, bad("invalid mention")
			}
			return &tg.TextMentionName{Text: text, UserID: ids.Client(ids.Server(id))}, nil
		}
		return &tg.TextURL{Text: text, URL: url}, nil
	case "span", "p", "div", "summary", "cite", "blockquote", "pre", "footer", "details", "h1", "h2", "h3", "h4", "h5", "h6", "aside", "tg-math-block", "hr":
		return text, nil
	default:
		return nil, bad("unsupported rich HTML tag: " + node.Data)
	}
}

func (v *view) richTableJSON(table *tg.PageBlockTable) map[string]any {
	var rows = []any{}
	var cells []any
	var row tg.PageTableRow
	var cell tg.PageTableCell
	var align, valign string
	var entry map[string]any
	for _, row = range table.Rows {
		cells = []any{}
		for _, cell = range row.Cells {
			align, valign = "left", "top"
			if cell.AlignCenter {
				align = "center"
			} else if cell.AlignRight {
				align = "right"
			}
			if cell.ValignMiddle {
				valign = "middle"
			} else if cell.ValignBottom {
				valign = "bottom"
			}
			entry = map[string]any{"text": v.richTextJSON(cell.Text), "is_header": cell.Header, "align": align, "valign": valign}
			if cell.Colspan > 1 {
				entry["colspan"] = cell.Colspan
			}
			if cell.Rowspan > 1 {
				entry["rowspan"] = cell.Rowspan
			}
			cells = append(cells, entry)
		}
		rows = append(rows, cells)
	}
	return map[string]any{"type": "table", "cells": rows, "is_bordered": table.Bordered, "is_striped": table.Striped, "is_compact": table.Compact, "caption": v.richTextJSON(table.Title)}
}
