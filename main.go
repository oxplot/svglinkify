package main

import (
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	_log "log"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

var (
	inkscapePath *string
	inputPath    string
	outputPath   string
	exportDPI    = flag.Int("dpi", 96, "Resolution for rasterization of filters")

	log = _log.New(os.Stderr, "", 0)

	anchorRegexp   = regexp.MustCompile(`<a\s[^>]*\bhref="([^">]+)"[^>]*>`)
	anchorIdRegexp = regexp.MustCompile(`\bid="([^"]+)"`)
	bboxRegexp     = regexp.MustCompile(`(?m)^([^,\n]+),([^,\n]+),([^,\n]+),([^,\n]+),([^,\n]+)$`)
)

type PositionedObject struct {
	// SVG ID
	ID string

	// X position of in pixels
	X float64

	// Y position of in pixels
	Y float64

	// Width of in pixels
	W float64

	// Height in pixels
	H float64
}

type PositionedLink struct {
	// SVG ID
	ID string

	// URL of the link
	URL string

	// X position of in pixels
	X float64

	// Y position of in pixels
	Y float64

	// Width of in pixels
	W float64

	// Height in pixels
	H float64

	// Valid indicates if this link has all the requirements to be used
	Valid bool
}

// BareFragment returns the ID portion of the URL, if the URL starts with #
// otherwise returns the empty string
func (l *PositionedLink) BareFragment() string {
	if l.URL[0] == '#' {
		return l.URL[1:]
	} else {
		return ""
	}
}

func init() {
	// Attempt to determine inkscape's path automatically
	defaultInkscapePath, _ := exec.LookPath("inkscape")
	inkscapePath = flag.String("inkscape-path", defaultInkscapePath, "path to inkscape binary")
	flag.Usage = func() {
		fmt.Fprint(flag.CommandLine.Output(), `
svglinkify converts SVGs to PDFs using inkscape while preserving hyperlinks
applied to objects. To use, first create an SVG file in inkscape. Create
hyperlinks for any object by right clicking the object and selecting "Create
Link". Enter a URL in Href field and save your SVG. Then use svglinkify to
convert your SVG file.

If the hyper link is '#some-id', an internal link is created which when
clicked, will pan and zoom onto the object with id 'some-id'.

Usage: svglinkify [options] input.svg output.pdf

`)
		flag.PrintDefaults()
	}
	flag.Parse()
	if len(flag.Args()) != 2 {
		flag.Usage()
		os.Exit(2)
	}
	inputPath = flag.Args()[0]
	outputPath = flag.Args()[1]
}

func readPDFObj(r io.Reader) (string, error) {
	objRegexp := regexp.MustCompile(`(?ms)^\d+\s+\d+\s+obj\s+(.*?)\s*^endobj$`)
	buf := make([]byte, 4096)
	if _, err := r.Read(buf); err != nil {
		return "", err
	}
	m := objRegexp.FindStringSubmatch(string(buf))
	if m == nil {
		return "", fmt.Errorf("cannot find read PDF object")
	}
	return m[1], nil
}

type PDFXrefEntry struct {
	Offset int64
	Gen    int
	Free   bool
}

func (e *PDFXrefEntry) Marshal(w io.Writer) (int, error) {
	var free string
	if e.Free {
		free = "f"
	} else {
		free = "n"
	}
	return fmt.Fprintf(w, "%010d %05d %s \n", e.Offset, e.Gen, free)
}

var PDFXrefFreeEntry = &PDFXrefEntry{Gen: 65535, Free: true}

type PDFObjRef struct {
	ID  int
	Gen int
}

func (r *PDFObjRef) String() string {
	return fmt.Sprintf("%d %d R", r.ID, r.Gen)
}

type PDFXrefTrailer struct {
	Size int
	Root *PDFObjRef
	Raw  string
}

func (t *PDFXrefTrailer) Marshal(w io.Writer) (int, error) {
	s := regexp.MustCompile(`/Size\s+\d+`).ReplaceAllStringFunc(t.Raw, func(s string) string {
		return fmt.Sprintf("/Size %d", t.Size)
	})
	s = regexp.MustCompile(`/Root\s+\d+\s+\d+\s+R`).ReplaceAllStringFunc(s, func(s string) string {
		return fmt.Sprintf("/Root %s", t.Root)
	})
	return w.Write([]byte("trailer\n" + s + "\n"))
}

type PDFCatalog struct {
	OwnRef   *PDFObjRef
	PagesRef *PDFObjRef
	Raw      string
}

func UnmarshalPDFCatalog(r io.Reader) (*PDFCatalog, error) {
	s, err := readPDFObj(r)
	if err != nil {
		return nil, err
	}
	re := regexp.MustCompile(`/Pages\s+(\d+)\s+(\d+)\s+R`)
	m := re.FindStringSubmatch(s)
	if m == nil {
		return nil, fmt.Errorf("cannot read PDF catalog")
	}
	id, _ := strconv.ParseInt(m[1], 10, 32)
	gen, _ := strconv.ParseInt(m[2], 10, 32)
	return &PDFCatalog{PagesRef: &PDFObjRef{ID: int(id), Gen: int(gen)}, Raw: s}, nil
}

func (c *PDFCatalog) Marshal(w io.Writer) (int, error) {
	s := regexp.MustCompile(`/Pages\s+\d+\s+\d+\s+R`).ReplaceAllStringFunc(c.Raw, func(s string) string {
		return fmt.Sprintf("/Pages %s", c.PagesRef)
	})

	return fmt.Fprintf(w, "%d %d obj\n%s\nendobj\n", c.OwnRef.ID, c.OwnRef.Gen, s)
}

type PDFPages struct {
	OwnRef   *PDFObjRef
	Page1Ref *PDFObjRef
	Raw      string
}

func UnmarshalPDFPages(r io.Reader) (*PDFPages, error) {
	s, err := readPDFObj(r)
	if err != nil {
		return nil, err
	}
	re := regexp.MustCompile(`/Kids\s+\[\s+(\d+)\s+(\d+)\s+R`)
	m := re.FindStringSubmatch(s)
	if m == nil {
		return nil, fmt.Errorf("cannot read PDF pages")
	}
	id, _ := strconv.ParseInt(m[1], 10, 32)
	gen, _ := strconv.ParseInt(m[2], 10, 32)
	return &PDFPages{Page1Ref: &PDFObjRef{ID: int(id), Gen: int(gen)}, Raw: s}, nil
}

func (p *PDFPages) Marshal(w io.Writer) (int, error) {
	s := regexp.MustCompile(`/Kids\s+\[\s+\d+\s+\d+\s+R`).ReplaceAllStringFunc(p.Raw, func(s string) string {
		return fmt.Sprintf("/Kids [ %s", p.Page1Ref)
	})

	return fmt.Fprintf(w, "%d %d obj\n%s\nendobj\n", p.OwnRef.ID, p.OwnRef.Gen, s)
}

type PDFPage struct {
	OwnRef  *PDFObjRef
	Links   []*PositionedLink
	Objects map[string]*PositionedObject
	Height  float64
	Raw     string
}

func UnmarshalPDFPage(r io.Reader) (*PDFPage, error) {
	s, err := readPDFObj(r)
	if err != nil {
		return nil, err
	}
	m := regexp.MustCompile(`/MediaBox\s+\[\s+\S+\s+\S+\s+\S+\s+(\S+)`).FindStringSubmatch(s)
	if m == nil {
		return nil, fmt.Errorf("cannot find PDF page media box")
	}
	h, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return nil, fmt.Errorf("invalid PDF media box height '%s' found", m[1])
	}
	return &PDFPage{Raw: s, Height: h}, nil
}

func (p *PDFPage) Marshal(w io.Writer) (int, error) {
	b := strings.Builder{}
	for _, l := range p.Links {
		bareFragLink := l.BareFragment()
		var action string
		if bareFragLink != "" {
			t := p.Objects[bareFragLink]
			if t == nil {
				action = ""
				log.Printf("link '%s' points to non-existing object", l.URL)
			} else {
				action = fmt.Sprintf("/GoTo /D [ %d %d R /FitR %f %f %f %f ]",
					p.OwnRef.ID, p.OwnRef.Gen, t.X*0.75, p.Height-(t.H+t.Y)*0.75, (t.W+t.X)*0.75, p.Height-t.Y*0.75)
			}
		} else {
			action = "/URI /URI (" + l.URL + ")"
		}
		b.WriteString(fmt.Sprintf(
			` << /Type /Annot /Subtype /Link /Border [ 0 0 0 ] /A << /S %s >> /Rect [ %f %f %f %f ] >> `,
			action, l.X*0.75, p.Height-l.Y*0.75, (l.W+l.X)*0.75, p.Height-(l.H+l.Y)*0.75,
		))
	}
	s := regexp.MustCompile(">>$").ReplaceAllStringFunc(p.Raw, func(s string) string {
		return fmt.Sprintf("/Annots [ %s ]\n>>", b.String())
	})
	return fmt.Fprintf(w, "%d %d obj\n%s\nendobj\n", p.OwnRef.ID, p.OwnRef.Gen, s)
}

func UnmarshalPDFXrefTrailer(s string) (*PDFXrefTrailer, error) {
	re := regexp.MustCompile(`/Root\s+(\d+)\s+(\d+)\s+R`)
	m := re.FindStringSubmatch(s)
	if m == nil {
		return nil, fmt.Errorf("cannot read PDF xref trailer")
	}
	id, _ := strconv.ParseInt(m[1], 10, 32)
	gen, _ := strconv.ParseInt(m[2], 10, 32)
	return &PDFXrefTrailer{Root: &PDFObjRef{ID: int(id), Gen: int(gen)}, Raw: s}, nil
}

type PDFXref struct {
	OwnOffset int64
	ObjStart  int
	ObjCount  int
	Entries   []*PDFXrefEntry
	Trailer   *PDFXrefTrailer
}

func UnmarshalPDFXref(r io.Reader) (*PDFXref, error) {
	buf, err := ioutil.ReadAll(r)
	if err != nil {
		return nil, err
	}
	re := regexp.MustCompile(`(?s)^xref\s+(\d+)\s+(\d+)\s+(.*?)\s+trailer\s+(.*?)\s+startxref\s+`)
	m := re.FindStringSubmatch(string(buf))
	if m == nil {
		return nil, fmt.Errorf("cannot find valid xref in PDF")
	}
	objStart, _ := strconv.ParseInt(m[1], 10, 32)
	objCount, _ := strconv.ParseInt(m[2], 10, 32)
	trailer, err := UnmarshalPDFXrefTrailer(m[4])
	if err != nil {
		return nil, err
	}
	xref := PDFXref{
		ObjStart: int(objStart),
		ObjCount: int(objCount),
		Trailer:  trailer,
	}
	re = regexp.MustCompile(`(?m)^(\d+)\s+(\d+)\s+([fn])[^\S\r\n]*$`)
	entriesM := re.FindAllStringSubmatch(m[3], -1)
	if entriesM == nil {
		return nil, fmt.Errorf("found empty xref")
	}
	for _, e := range entriesM {
		offset, _ := strconv.ParseInt(e[1], 10, 64)
		gen, _ := strconv.ParseInt(e[2], 10, 32)
		entry := PDFXrefEntry{
			Offset: offset,
			Gen:    int(gen),
			Free:   e[3] == "f",
		}
		xref.Entries = append(xref.Entries, &entry)
	}
	return &xref, nil
}

func (x *PDFXref) Marshal(w io.Writer) (int, error) {
	var err error
	var nTotal, n int
	if n, err = fmt.Fprintf(w, "xref\n0 %d\n", len(x.Entries)); err != nil {
		return nTotal + n, err
	}
	nTotal += n

	for _, e := range x.Entries {
		if n, err = e.Marshal(w); err != nil {
			return nTotal + n, err
		}
		nTotal += n
	}

	if n, err = x.Trailer.Marshal(w); err != nil {
		return nTotal + n, err
	}
	nTotal += n

	return nTotal, nil
}

// addLinksToPDF incrementally updates the PDF output of inkscape to add
// clickable links
func addLinksToPDF(f io.ReadWriteSeeker, allObjects map[string]*PositionedObject, links []*PositionedLink) error {
	var err error
	startxrefRegexp := regexp.MustCompile(`(?m)^startxref\s+(\d+)`)

	// Load original xref, catalog, pages and page 1 of the PDF

	buf := make([]byte, 50)
	f.Seek(-50, io.SeekEnd)
	if _, err := f.Read(buf); err != nil {
		return err
	}
	sxrefM := startxrefRegexp.FindStringSubmatch(string(buf))
	if sxrefM == nil {
		return fmt.Errorf("cannot find startxref in PDF")
	}
	origXrefOff, _ := strconv.ParseInt(sxrefM[1], 10, 64)

	f.Seek(origXrefOff, io.SeekStart)

	xref, err := UnmarshalPDFXref(f)
	if err != nil {
		return err
	}
	xref.OwnOffset = origXrefOff

	f.Seek(xref.Entries[xref.Trailer.Root.ID].Offset, io.SeekStart)
	catalog, err := UnmarshalPDFCatalog(f)
	if err != nil {
		return err
	}
	catalog.OwnRef = xref.Trailer.Root

	f.Seek(xref.Entries[catalog.PagesRef.ID].Offset, io.SeekStart)
	pages, err := UnmarshalPDFPages(f)
	if err != nil {
		return err
	}
	pages.OwnRef = catalog.PagesRef

	f.Seek(xref.Entries[pages.Page1Ref.ID].Offset, io.SeekStart)
	page1, err := UnmarshalPDFPage(f)
	if err != nil {
		return err
	}
	page1.OwnRef = pages.Page1Ref

	// Update the page 1 with the new links and objects

	page1.Links = links
	page1.Objects = allObjects

	// Write new catalog, pages, and page 1

	var outN int

	nextOff := xref.OwnOffset
	page1Off := nextOff
	xref.Entries[page1.OwnRef.ID] = PDFXrefFreeEntry
	page1.OwnRef = &PDFObjRef{ID: len(xref.Entries)}
	f.Seek(nextOff, io.SeekStart)
	if outN, err = page1.Marshal(f); err != nil {
		return err
	}

	nextOff += int64(outN)
	pagesOff := nextOff
	pages.Page1Ref = page1.OwnRef
	xref.Entries[pages.OwnRef.ID] = PDFXrefFreeEntry
	pages.OwnRef = &PDFObjRef{ID: len(xref.Entries) + 1}
	if outN, err = pages.Marshal(f); err != nil {
		return err
	}

	nextOff += int64(outN)
	catalogOff := nextOff
	catalog.PagesRef = pages.OwnRef
	xref.Entries[catalog.OwnRef.ID] = PDFXrefFreeEntry
	catalog.OwnRef = &PDFObjRef{ID: len(xref.Entries) + 2}
	if outN, err = catalog.Marshal(f); err != nil {
		return err
	}

	// Write back updated original xref

	nextOff += int64(outN)
	xrefNewOff := nextOff
	xref.Entries = append(xref.Entries, &PDFXrefEntry{Offset: page1Off})
	xref.Entries = append(xref.Entries, &PDFXrefEntry{Offset: pagesOff})
	xref.Entries = append(xref.Entries, &PDFXrefEntry{Offset: catalogOff})
	xref.Trailer.Root = catalog.OwnRef
	xref.Trailer.Size = len(xref.Entries)

	if _, err = xref.Marshal(f); err != nil {
		return err
	}

	if _, err := fmt.Fprintf(f, "startxref\n%d\n%%EOF", xrefNewOff); err != nil {
		return err
	}

	return nil
}

func main() {

	// Load the SVG file

	svgContent := func() string {
		f, err := os.Open(inputPath)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		v, err := ioutil.ReadAll(f)
		if err != nil {
			log.Fatal(err)
		}
		return string(v)
	}()

	links := []*PositionedLink{}

	// Find all the anchor elements and extract their id and links.

	anchorMatches := anchorRegexp.FindAllStringSubmatch(svgContent, -1)

	for _, a := range anchorMatches {
		l := PositionedLink{URL: a[1]}
		idm := anchorIdRegexp.FindStringSubmatch(a[0])
		if idm == nil {
			continue
		}
		l.ID = idm[1]
		links = append(links, &l)
	}

	if len(links) == 0 {
		log.Print("did not find any links")
	}

	// Determine the final bounding boxes of all the links

	inkBBoxOut, err := exec.Command(*inkscapePath, "-S", inputPath).Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Stderr.Write(exitErr.Stderr)
			log.Fatal("inkscape errored when calculating bounding boxes")
		}
		log.Fatal(err)
	}
	bboxMatches := bboxRegexp.FindAllStringSubmatch(string(inkBBoxOut), -1)

	// Parse all bounding box as objects
	allObjects := map[string]*PositionedObject{}

	for _, bb := range bboxMatches {
		o := PositionedObject{ID: bb[1]}
		o.X, err = strconv.ParseFloat(bb[2], 64)
		if err != nil {
			log.Printf("inkscape gave us '%s' which is invalid as X for id '%s' - ignoring object", bb[2], o.ID)
			continue
		}
		o.Y, err = strconv.ParseFloat(bb[3], 64)
		if err != nil {
			log.Printf("inkscape gave us '%s' which is invalid as Y for '%s' - ignoring object", bb[3], o.ID)
			continue
		}
		o.W, err = strconv.ParseFloat(bb[4], 64)
		if err != nil {
			log.Printf("inkscape gave us '%s' which is invalid as W for '%s' - ignoring object", bb[4], o.ID)
			continue
		}
		o.H, err = strconv.ParseFloat(bb[5], 64)
		if err != nil {
			log.Printf("inkscape gave us '%s' which is invalid as W for '%s' - ignoring object", bb[5], o.ID)
			continue
		}
		allObjects[o.ID] = &o
	}

	for _, l := range links {
		if o, ok := allObjects[l.ID]; ok {
			l.X, l.Y, l.W, l.H = o.X, o.Y, o.W, o.H
			l.Valid = true
		} else {
			log.Print("inkscape didn't tell us the bounding box for link '%s' - ignoring link", l.URL)
		}
	}

	validLinks := links[:0]
	for _, l := range links {
		if l.Valid {
			validLinks = append(validLinks, l)
		}
	}

	// Generate the PDF
	args := []string{
		"--export-dpi", strconv.Itoa(*exportDPI),
		"--export-pdf", outputPath,
		inputPath,
	}
	if err := exec.Command(*inkscapePath, args...).Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Stderr.Write(exitErr.Stderr)
			log.Fatal("inkscape errored while generating PDF")
		}
		log.Fatal(err)
	}

	// Add links to PDF

	func() {
		f, err := os.OpenFile(outputPath, os.O_RDWR, 0666)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		if err := addLinksToPDF(f, allObjects, validLinks); err != nil {
			log.Fatal(err)
		}
	}()
}
