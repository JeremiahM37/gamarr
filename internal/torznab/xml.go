// Package torznab serves the Torznab/Newznab indexer API so Gamarr can be
// added as an indexer in Prowlarr / Sonarr / other *arr apps.
package torznab

import "encoding/xml"

// RSS is the root of a Torznab search response.
type RSS struct {
	XMLName xml.Name `xml:"rss"`
	Version string   `xml:"version,attr"`
	Xmlns   string   `xml:"xmlns:torznab,attr"`
	Channel Channel  `xml:"channel"`
}

type Channel struct {
	Title       string `xml:"title"`
	Description string `xml:"description,omitempty"`
	Items       []Item `xml:"item"`
}

type Item struct {
	Title     string     `xml:"title"`
	GUID      string     `xml:"guid"`
	Link      string     `xml:"link,omitempty"`
	Size      int64      `xml:"size,omitempty"`
	Category  string     `xml:"category,omitempty"`
	PubDate   string     `xml:"pubDate,omitempty"`
	Enclosure *Enclosure `xml:"enclosure,omitempty"`
	Attrs     []Attr     `xml:"torznab:attr,omitempty"`
}

type Enclosure struct {
	URL    string `xml:"url,attr"`
	Length int64  `xml:"length,attr,omitempty"`
	Type   string `xml:"type,attr"`
}

type Attr struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

// Caps is the Torznab capabilities document.
type Caps struct {
	XMLName    xml.Name       `xml:"caps"`
	Server     CapsServer     `xml:"server"`
	Limits     CapsLimits     `xml:"limits"`
	Searching  CapsSearching  `xml:"searching"`
	Categories CapsCategories `xml:"categories"`
}

type CapsServer struct {
	Title string `xml:"title,attr"`
}
type CapsLimits struct {
	Max     int `xml:"max,attr"`
	Default int `xml:"default,attr"`
}
type CapsSearching struct {
	Search        CapsSearchOp `xml:"search"`
	ConsoleSearch CapsSearchOp `xml:"console-search"`
	PCSearch      CapsSearchOp `xml:"pc-search"`
}
type CapsSearchOp struct {
	Available       string `xml:"available,attr"`
	SupportedParams string `xml:"supportedParams,attr"`
}
type CapsCategories struct {
	Categories []CapsCategory `xml:"category"`
}
type CapsCategory struct {
	ID   string            `xml:"id,attr"`
	Name string            `xml:"name,attr"`
	Subs []CapsSubCategory `xml:"subcat,omitempty"`
}
type CapsSubCategory struct {
	ID   string `xml:"id,attr"`
	Name string `xml:"name,attr"`
}

// Error is the Torznab error document.
type Error struct {
	XMLName     xml.Name `xml:"error"`
	Code        string   `xml:"code,attr"`
	Description string   `xml:"description,attr"`
}
