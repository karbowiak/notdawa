package dawa

import "fmt"

// RegionRef is DAWA's nested region reference, embedded in kommuner, landsdele,
// and other entities that belong to a region.
type RegionRef struct {
	Href string `json:"href"`
	Kode string `json:"kode"`
	Navn string `json:"navn"`
}

// newRegionRef builds a RegionRef from a (nullable) region kode + navn. Returns
// nil when there is no related region (e.g. an unresolved join).
func newRegionRef(baseURL string, kode, navn *string) *RegionRef {
	if kode == nil {
		return nil
	}
	var n string
	if navn != nil {
		n = *navn
	}
	return &RegionRef{
		Href: fmt.Sprintf("%s/regioner/%s", baseURL, *kode),
		Kode: *kode,
		Navn: n,
	}
}

// KommuneRef is DAWA's nested kommune reference (administrerendekommune, a
// vejstykke's kommune, a vejnavn's kommuner[]).
type KommuneRef struct {
	Href string `json:"href"`
	Kode string `json:"kode"`
	Navn string `json:"navn"`
}

// newKommuneRef builds a KommuneRef from a (nullable) kommunekode + navn. The
// kode is kept exactly as stored (4-char zero-padded). Returns nil when there is
// no related kommune.
func newKommuneRef(baseURL string, kode, navn *string) *KommuneRef {
	if kode == nil {
		return nil
	}
	var n string
	if navn != nil {
		n = *navn
	}
	return &KommuneRef{
		Href: fmt.Sprintf("%s/kommuner/%s", baseURL, *kode),
		Kode: *kode,
		Navn: n,
	}
}

// PostnrRef is DAWA's nested postnummer reference {href,nr,navn} used by the road
// entities (the address /postnumre entity is a separate, richer shape).
type PostnrRef struct {
	Href string  `json:"href"`
	Nr   string  `json:"nr"`
	Navn *string `json:"navn"`
}

// VejstykkeMiniRef is the nested vejstykke reference inside a navngivenvej's
// vejstykker[]. Its href keeps the kommunekode 4-char zero-padded (unlike the
// /vejstykker entity href, which strips leading zeros).
type VejstykkeMiniRef struct {
	Href        string `json:"href"`
	Kommunekode string `json:"kommunekode"`
	Kode        string `json:"kode"`
	ID          string `json:"id"`
	Darstatus   int    `json:"darstatus"`
}

// NavngivenvejMiniRef is the nested navngivenvej reference inside a vejstykke.
type NavngivenvejMiniRef struct {
	Href      string `json:"href"`
	ID        string `json:"id"`
	Darstatus int    `json:"darstatus"`
}

// ValglandsdelRef is DAWA's nested valglandsdel reference embedded in storkredse.
type ValglandsdelRef struct {
	Href    string `json:"href"`
	Bogstav string `json:"bogstav"`
	Navn    string `json:"navn"`
}

// newValglandsdelRef builds a ValglandsdelRef from a (nullable) bogstav + navn.
func newValglandsdelRef(baseURL string, bogstav, navn *string) *ValglandsdelRef {
	if bogstav == nil {
		return nil
	}
	var n string
	if navn != nil {
		n = *navn
	}
	return &ValglandsdelRef{
		Href:    fmt.Sprintf("%s/valglandsdele/%s", baseURL, *bogstav),
		Bogstav: *bogstav,
		Navn:    n,
	}
}

// StorkredsRef is DAWA's nested storkreds reference (in opstillingskredse and
// afstemningsomraader); note nummer, not kode.
type StorkredsRef struct {
	Href   string `json:"href"`
	Nummer string `json:"nummer"`
	Navn   string `json:"navn"`
}

// newStorkredsRef builds a StorkredsRef from a (nullable) nummer + navn.
func newStorkredsRef(baseURL string, nummer, navn *string) *StorkredsRef {
	if nummer == nil {
		return nil
	}
	var n string
	if navn != nil {
		n = *navn
	}
	return &StorkredsRef{
		Href:   fmt.Sprintf("%s/storkredse/%s", baseURL, *nummer),
		Nummer: *nummer,
		Navn:   n,
	}
}

// OpstillingskredsRef is DAWA's nested opstillingskreds reference (in
// afstemningsomraader); note nummer, not kode.
type OpstillingskredsRef struct {
	Href   string `json:"href"`
	Nummer string `json:"nummer"`
	Navn   string `json:"navn"`
}

// newOpstillingskredsRef builds an OpstillingskredsRef from a nummer + navn.
func newOpstillingskredsRef(baseURL string, nummer, navn *string) *OpstillingskredsRef {
	if nummer == nil {
		return nil
	}
	var n string
	if navn != nil {
		n = *navn
	}
	return &OpstillingskredsRef{
		Href:   fmt.Sprintf("%s/opstillingskredse/%s", baseURL, *nummer),
		Nummer: *nummer,
		Navn:   n,
	}
}
