// Package formats reconnaît le type des fichiers déposés dans Jarvis
// (jalon 25 : au-delà du PDF, tout ce qu'on stockerait dans un Google
// Drive) et produit, pour chaque famille, une version PDF qui alimente
// l'aperçu, la miniature et le pipeline existant (triage → VLM → LLM),
// sans que ce dernier ait à connaître d'autre format que le PDF.
package formats

import (
	"bytes"
	"path/filepath"
	"strings"
)

// Family regroupe les formats qui partagent un même traitement.
type Family string

const (
	PDF    Family = "pdf"
	Word   Family = "word"   // doc, docx, odt, rtf, pages
	Slides Family = "slides" // ppt, pptx, odp, key
	Sheet  Family = "sheet"  // xls, xlsx, xlsm, xlsb, ods, numbers
	CSV    Family = "csv"    // csv, tsv
	Text   Family = "text"   // txt, md, json, xml, code, logs
	HTML   Family = "html"
	Image  Family = "image" // jpg, png, heic, tiff, webp, gif, bmp
	Email  Family = "email" // eml
	Other  Family = "other" // stocké tel quel : zip, dmg, dcm...
)

// NeedsRendition indique si la famille produit une version PDF (tout
// sauf le PDF lui-même et les fichiers seulement stockés).
func (f Family) NeedsRendition() bool {
	return f != PDF && f != Other
}

// Pipeline indique si la famille passe par classification + extraction
// (décision de l'utilisateur, jalon 25 : tous les documents lisibles).
func (f Family) Pipeline() bool {
	return f != Other
}

// Label est le nom de la famille affiché dans l'interface.
func (f Family) Label() string {
	switch f {
	case PDF:
		return "PDF"
	case Word:
		return "Document"
	case Slides:
		return "Présentation"
	case Sheet:
		return "Tableur"
	case CSV:
		return "CSV"
	case Text:
		return "Texte"
	case HTML:
		return "Page web"
	case Image:
		return "Image"
	case Email:
		return "E-mail"
	default:
		return "Fichier"
	}
}

// Format est le résultat de la détection : famille, type MIME (servi au
// téléchargement) et extension en minuscules, sans point.
type Format struct {
	Family Family
	MIME   string
	Ext    string
}

type extInfo struct {
	family Family
	mime   string
}

var byExt = map[string]extInfo{
	"pdf": {PDF, "application/pdf"},

	"doc":   {Word, "application/msword"},
	"dot":   {Word, "application/msword"},
	"docx":  {Word, "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
	"docm":  {Word, "application/vnd.ms-word.document.macroEnabled.12"},
	"dotx":  {Word, "application/vnd.openxmlformats-officedocument.wordprocessingml.template"},
	"odt":   {Word, "application/vnd.oasis.opendocument.text"},
	"ott":   {Word, "application/vnd.oasis.opendocument.text-template"},
	"rtf":   {Word, "application/rtf"},
	"pages": {Word, "application/vnd.apple.pages"},

	"ppt":  {Slides, "application/vnd.ms-powerpoint"},
	"pps":  {Slides, "application/vnd.ms-powerpoint"},
	"pot":  {Slides, "application/vnd.ms-powerpoint"},
	"pptx": {Slides, "application/vnd.openxmlformats-officedocument.presentationml.presentation"},
	"pptm": {Slides, "application/vnd.ms-powerpoint.presentation.macroEnabled.12"},
	"ppsx": {Slides, "application/vnd.openxmlformats-officedocument.presentationml.slideshow"},
	"potx": {Slides, "application/vnd.openxmlformats-officedocument.presentationml.template"},
	"odp":  {Slides, "application/vnd.oasis.opendocument.presentation"},
	"otp":  {Slides, "application/vnd.oasis.opendocument.presentation-template"},
	"key":  {Slides, "application/vnd.apple.keynote"},

	"xls":     {Sheet, "application/vnd.ms-excel"},
	"xlt":     {Sheet, "application/vnd.ms-excel"},
	"xlsx":    {Sheet, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
	"xlsm":    {Sheet, "application/vnd.ms-excel.sheet.macroEnabled.12"},
	"xlsb":    {Sheet, "application/vnd.ms-excel.sheet.binary.macroEnabled.12"},
	"xltx":    {Sheet, "application/vnd.openxmlformats-officedocument.spreadsheetml.template"},
	"xltm":    {Sheet, "application/vnd.ms-excel.template.macroEnabled.12"},
	"ods":     {Sheet, "application/vnd.oasis.opendocument.spreadsheet"},
	"ots":     {Sheet, "application/vnd.oasis.opendocument.spreadsheet-template"},
	"numbers": {Sheet, "application/vnd.apple.numbers"},

	"csv": {CSV, "text/csv"},
	"tsv": {CSV, "text/tab-separated-values"},

	"txt":      {Text, "text/plain"},
	"text":     {Text, "text/plain"},
	"log":      {Text, "text/plain"},
	"md":       {Text, "text/markdown"},
	"markdown": {Text, "text/markdown"},
	"json":     {Text, "application/json"},
	"xml":      {Text, "application/xml"},
	"yaml":     {Text, "application/yaml"},
	"yml":      {Text, "application/yaml"},
	"ini":      {Text, "text/plain"},
	"cfg":      {Text, "text/plain"},
	"conf":     {Text, "text/plain"},
	"ics":      {Text, "text/calendar"},
	"vcf":      {Text, "text/vcard"},
	"sql":      {Text, "text/plain"},
	"go":       {Text, "text/plain"},
	"py":       {Text, "text/plain"},
	"js":       {Text, "text/plain"},
	"ts":       {Text, "text/plain"},
	"sh":       {Text, "text/plain"},
	"css":      {Text, "text/plain"},
	"java":     {Text, "text/plain"},
	"c":        {Text, "text/plain"},
	"h":        {Text, "text/plain"},

	"html":  {HTML, "text/html"},
	"htm":   {HTML, "text/html"},
	"xhtml": {HTML, "text/html"},

	"jpg":  {Image, "image/jpeg"},
	"jpeg": {Image, "image/jpeg"},
	"png":  {Image, "image/png"},
	"gif":  {Image, "image/gif"},
	"webp": {Image, "image/webp"},
	"heic": {Image, "image/heic"},
	"heif": {Image, "image/heif"},
	"tif":  {Image, "image/tiff"},
	"tiff": {Image, "image/tiff"},
	"bmp":  {Image, "image/bmp"},

	"eml": {Email, "message/rfc822"},

	"zip": {Other, "application/zip"},
	"dmg": {Other, "application/x-apple-diskimage"},
	"7z":  {Other, "application/x-7z-compressed"},
	"rar": {Other, "application/vnd.rar"},
	"gz":  {Other, "application/gzip"},
	"tar": {Other, "application/x-tar"},
	"dcm": {Other, "application/dicom"},
	"mp4": {Other, "video/mp4"},
	"mov": {Other, "video/quicktime"},
	"mp3": {Other, "audio/mpeg"},
	"m4a": {Other, "audio/mp4"},
}

// Detect reconnaît le format d'un fichier d'après son nom et ses premiers
// octets (head, nil accepté). L'extension fait foi quand elle est connue
// — c'est ce que l'utilisateur voit — sauf pour un contenu PDF, reconnu
// sans ambiguïté par sa signature. Sans extension connue, le contenu
// tranche : PDF, image courante, texte, sinon fichier stocké tel quel.
func Detect(filename string, head []byte) Format {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(filename), "."))
	if bytes.HasPrefix(head, []byte("%PDF-")) {
		return Format{Family: PDF, MIME: "application/pdf", Ext: ext}
	}
	if info, ok := byExt[ext]; ok {
		return Format{Family: info.family, MIME: info.mime, Ext: ext}
	}
	if mime, ok := sniffImage(head); ok {
		return Format{Family: Image, MIME: mime, Ext: ext}
	}
	if len(head) > 0 && looksLikeText(head) {
		return Format{Family: Text, MIME: "text/plain", Ext: ext}
	}
	return Format{Family: Other, MIME: "application/octet-stream", Ext: ext}
}

func sniffImage(head []byte) (string, bool) {
	switch {
	case bytes.HasPrefix(head, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png", true
	case bytes.HasPrefix(head, []byte("\xff\xd8\xff")):
		return "image/jpeg", true
	case bytes.HasPrefix(head, []byte("GIF87a")), bytes.HasPrefix(head, []byte("GIF89a")):
		return "image/gif", true
	case len(head) >= 12 && bytes.Equal(head[:4], []byte("RIFF")) && bytes.Equal(head[8:12], []byte("WEBP")):
		return "image/webp", true
	}
	return "", false
}

// looksLikeText : aucun octet NUL et moins de 10 % de caractères de
// contrôle (hors tabulation, retours à la ligne, saut de page). Accepte
// l'UTF-8 comme le Windows-1252 (courant pour un texte français sans
// extension) — le décodage exact est l'affaire de l'aperçu.
func looksLikeText(head []byte) bool {
	if bytes.IndexByte(head, 0) >= 0 {
		return false
	}
	control := 0
	for _, b := range head {
		if b < 0x20 && b != '\t' && b != '\n' && b != '\r' && b != '\f' {
			control++
		}
	}
	return control*10 < len(head)
}
