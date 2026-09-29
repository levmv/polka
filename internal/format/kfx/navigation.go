package kfx

import (
	"fmt"
	"net/url"
	"strings"
)

type position struct {
	id     string
	offset int
}

func positionOf(v value) (position, error) {
	id := locationID(v)
	offset := v.get(fieldOffset).integer
	if id == "" || offset < 0 || offset > maxGeneratedBytes {
		return position{}, invalid("invalid navigation position")
	}
	return position{id, int(offset)}, nil
}

func (c *contentReader) anchor(p position) string {
	if id := c.anchors[p]; id != "" {
		return id
	}
	id := fmt.Sprintf("position-%d", len(c.anchors)+1)
	c.anchors[p] = id
	return id
}

func (c *contentReader) navigation() error {
	for _, f := range c.book.ordered {
		if f.key.kind != fragmentLink {
			continue
		}
		if err := c.book.ctx.Err(); err != nil {
			return err
		}
		v, err := c.book.load(f)
		if err != nil {
			if !c.recover(err, "Ignored a damaged KFX link destination.") {
				return err
			}
			continue
		}
		if uri := v.get(fieldURI).text; uri != "" {
			u, err := url.Parse(uri)
			if err == nil && (u.Scheme == "http" || u.Scheme == "https" || u.Scheme == "mailto") {
				c.externalLinks[f.key.id] = uri
			}
		} else if v.get(fieldLinkPosition).kind != ionPadding {
			p, err := positionOf(v.get(fieldLinkPosition))
			if err != nil {
				c.warning("Ignored an invalid KFX link destination.")
				continue
			}
			c.anchorTargets[f.key.id] = p
		}
	}
	root, err := c.book.singleton(fragmentNavigation)
	if err != nil {
		if c.recover(err, "Could not read KFX navigation; kept the reading content.") {
			return nil
		}
		return err
	}
	for _, order := range root.list {
		for _, reference := range order.get(fieldNavContainers).list {
			v, err := c.book.resolve(fragmentNavContainer, reference)
			if err != nil {
				if !c.recover(err, "Ignored a damaged KFX navigation group.") {
					return err
				}
				continue
			}
			kind := v.get(fieldNavType).text
			if kind != symbolTOC && kind != symbolPageList {
				continue
			}
			items, err := c.navItems(v.get(fieldNavChildren).list, 0)
			if err != nil {
				return err
			}
			if kind == symbolPageList {
				c.doc.Pages = append(c.doc.Pages, items...)
			} else {
				c.doc.Navigation = append(c.doc.Navigation, items...)
			}
		}
	}
	return nil
}

func (c *contentReader) navItems(values []value, depth int) ([]NavItem, error) {
	if depth > 32 {
		return nil, fmt.Errorf("%w: navigation is too deeply nested", ErrLimit)
	}
	var items []NavItem
	for _, ref := range values {
		if err := c.book.ctx.Err(); err != nil {
			return nil, err
		}
		c.navCount++
		if c.navCount > 16384 {
			return nil, fmt.Errorf("%w: too many navigation entries", ErrLimit)
		}
		v, err := c.book.resolve(fragmentNavItem, ref)
		if err != nil {
			if !c.recover(err, "Ignored a damaged KFX navigation entry.") {
				return nil, err
			}
			continue
		}
		children, err := c.navItems(v.get(fieldNavChildren).list, depth+1)
		if err != nil {
			return nil, err
		}
		label := strings.TrimSpace(v.get(fieldNavLabel).get(fieldNavText).text)
		if label == "" {
			items = append(items, children...)
			continue
		}
		href := ""
		if target := v.get(fieldNavPosition); target.kind != ionPadding {
			p, err := positionOf(target)
			if err != nil {
				c.warning("Ignored an invalid KFX navigation destination.")
				items = append(items, children...)
				continue
			}
			href = "#" + c.anchor(p)
		}
		// Referenced entries may repeat a large label without repeating its
		// source bytes. Reserve its escaped output before EPUB packaging.
		if err := c.claimGenerated(escapedSize(label) + len(href) + 64); err != nil {
			return nil, err
		}
		items = append(items, NavItem{Label: label, Href: href, Children: children})
	}
	return items, nil
}

func (c *contentReader) link(name string) string {
	if uri := c.externalLinks[name]; uri != "" {
		return uri
	}
	if p, ok := c.anchorTargets[name]; ok {
		return "#" + c.anchor(p)
	}
	c.warning("A KFX link has no usable destination; kept its text.")
	return ""
}
