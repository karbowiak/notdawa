package dawa

// struktur.go builds DAWA's struktur=mini representation for adresser and
// adgangsadresser. (struktur=nestet is the default full object; struktur=flad is
// the wide flat projection built elsewhere.) DAWA's mini shape is the flat address
// projection — the same fields the aggregate /autocomplete emits as its `data`
// element, in the same order — followed by a trailing `betegnelse`. The field
// VALUES are taken from flatFromAdgangsadresse/flatFromAdresse (already verified
// byte-identical to that projection); only the JSON shape differs:
//
//   - adgangsadresse mini omits etage/dør/adgangsadresseid entirely.
//   - adresse mini ALWAYS carries etage/dør/adgangsadresseid (null when absent) —
//     unlike the autocomplete data's omitempty — so a collection of single-unit
//     addresses still emits the keys as null, matching DAWA.

// AdgangsadresseMiniObj is the struktur=mini shape for an adgangsadresse.
type AdgangsadresseMiniObj struct {
	ID                     string   `json:"id"`
	Status                 int      `json:"status"`
	Darstatus              int      `json:"darstatus"`
	Vejkode                string   `json:"vejkode"`
	Vejnavn                *string  `json:"vejnavn"`
	Adresseringsvejnavn    *string  `json:"adresseringsvejnavn"`
	Husnr                  string   `json:"husnr"`
	Supplerendebynavn      *string  `json:"supplerendebynavn"`
	Postnr                 *string  `json:"postnr"`
	Postnrnavn             *string  `json:"postnrnavn"`
	Stormodtagerpostnr     *string  `json:"stormodtagerpostnr"`
	Stormodtagerpostnrnavn *string  `json:"stormodtagerpostnrnavn"`
	Kommunekode            string   `json:"kommunekode"`
	X                      *float64 `json:"x"`
	Y                      *float64 `json:"y"`
	Href                   string   `json:"href"`
	Betegnelse             string   `json:"betegnelse"`
}

// AdresseMiniObj is the struktur=mini shape for an adresse: as above plus
// etage/dør (after husnr) and adgangsadresseid (after kommunekode), always present.
type AdresseMiniObj struct {
	ID                     string   `json:"id"`
	Status                 int      `json:"status"`
	Darstatus              int      `json:"darstatus"`
	Vejkode                string   `json:"vejkode"`
	Vejnavn                *string  `json:"vejnavn"`
	Adresseringsvejnavn    *string  `json:"adresseringsvejnavn"`
	Husnr                  string   `json:"husnr"`
	Etage                  *string  `json:"etage"`
	Doer                   *string  `json:"dør"`
	Supplerendebynavn      *string  `json:"supplerendebynavn"`
	Postnr                 *string  `json:"postnr"`
	Postnrnavn             *string  `json:"postnrnavn"`
	Stormodtagerpostnr     *string  `json:"stormodtagerpostnr"`
	Stormodtagerpostnrnavn *string  `json:"stormodtagerpostnrnavn"`
	Kommunekode            string   `json:"kommunekode"`
	Adgangsadresseid       *string  `json:"adgangsadresseid"`
	X                      *float64 `json:"x"`
	Y                      *float64 `json:"y"`
	Href                   string   `json:"href"`
	Betegnelse             string   `json:"betegnelse"`
}

// AdgangsadresseMini projects a full adgangsadresse to its struktur=mini shape.
func AdgangsadresseMini(a *Adgangsadresse) *AdgangsadresseMiniObj {
	d := flatFromAdgangsadresse(a)
	return &AdgangsadresseMiniObj{
		ID: d.ID, Status: d.Status, Darstatus: d.Darstatus, Vejkode: d.Vejkode,
		Vejnavn: d.Vejnavn, Adresseringsvejnavn: d.Adresseringsvejnavn, Husnr: d.Husnr,
		Supplerendebynavn: d.Supplerendebynavn, Postnr: d.Postnr, Postnrnavn: d.Postnrnavn,
		Stormodtagerpostnr: d.Stormodtagerpostnr, Stormodtagerpostnrnavn: d.Stormodtagerpostnrnavn,
		Kommunekode: d.Kommunekode, X: d.X, Y: d.Y, Href: d.Href, Betegnelse: a.Adressebetegnelse,
	}
}

// AdresseMini projects a full adresse to its struktur=mini shape.
func AdresseMini(a *Adresse) *AdresseMiniObj {
	d := flatFromAdresse(a)
	return &AdresseMiniObj{
		ID: d.ID, Status: d.Status, Darstatus: d.Darstatus, Vejkode: d.Vejkode,
		Vejnavn: d.Vejnavn, Adresseringsvejnavn: d.Adresseringsvejnavn, Husnr: d.Husnr,
		Etage: d.Etage, Doer: d.Doer,
		Supplerendebynavn: d.Supplerendebynavn, Postnr: d.Postnr, Postnrnavn: d.Postnrnavn,
		Stormodtagerpostnr: d.Stormodtagerpostnr, Stormodtagerpostnrnavn: d.Stormodtagerpostnrnavn,
		Kommunekode: d.Kommunekode, Adgangsadresseid: d.Adgangsadresseid,
		X: d.X, Y: d.Y, Href: d.Href, Betegnelse: a.Adressebetegnelse,
	}
}
