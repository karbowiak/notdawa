// Package geo holds geometry helpers that PostGIS cannot reproduce byte-for-byte.
//
// The pole of inaccessibility (DAWA's "visueltcenter") is computed by DAWA with
// Mapbox's polylabel at 1 m precision, NOT PostGIS ST_MaximumInscribedCircle —
// the two diverge by kilometres on multi-part coastal geometries (e.g. the
// Hovedstaden landmass). This is a faithful Go port of @mapbox/polylabel so the
// election entities (storkredse/valglandsdele) match DAWA exactly; it can also
// tighten the DAGI/MAT area entities, which currently accept the GEOS gap.
package geo

import (
	"container/heap"
	"math"
)

// Point is an [x, y] coordinate (EPSG:25832 metres for our callers).
type Point = [2]float64

// Ring is a closed linear ring; Polygon is [outer, hole1, hole2, ...].
type Ring = []Point
type Polygon = []Ring

const sqrt2 = 1.4142135623730951

// Polylabel returns the pole of inaccessibility of a polygon (with holes) at the
// given precision, matching @mapbox/polylabel. precision is in the coordinate
// unit (metres in 25832).
func Polylabel(polygon Polygon, precision float64) Point {
	if len(polygon) == 0 || len(polygon[0]) == 0 {
		return Point{0, 0}
	}

	// Bounding box of the outer ring.
	minX, minY := polygon[0][0][0], polygon[0][0][1]
	maxX, maxY := minX, minY
	for _, p := range polygon[0] {
		if p[0] < minX {
			minX = p[0]
		}
		if p[1] < minY {
			minY = p[1]
		}
		if p[0] > maxX {
			maxX = p[0]
		}
		if p[1] > maxY {
			maxY = p[1]
		}
	}

	width := maxX - minX
	height := maxY - minY
	cellSize := math.Min(width, height)
	if cellSize == 0 {
		return Point{minX, minY}
	}
	h := cellSize / 2

	cq := &cellQueue{}
	heap.Init(cq)

	// Cover the polygon with initial cells.
	for x := minX; x < maxX; x += cellSize {
		for y := minY; y < maxY; y += cellSize {
			heap.Push(cq, newCell(x+h, y+h, h, polygon))
		}
	}

	// First guess: centroid; second: bbox centre.
	best := getCentroidCell(polygon)
	bboxCell := newCell(minX+width/2, minY+height/2, 0, polygon)
	if bboxCell.d > best.d {
		best = bboxCell
	}

	for cq.Len() > 0 {
		cell := heap.Pop(cq).(*cell)
		if cell.d > best.d {
			best = cell
		}
		if cell.max-best.d <= precision {
			continue
		}
		h = cell.h / 2
		heap.Push(cq, newCell(cell.x-h, cell.y-h, h, polygon))
		heap.Push(cq, newCell(cell.x+h, cell.y-h, h, polygon))
		heap.Push(cq, newCell(cell.x-h, cell.y+h, h, polygon))
		heap.Push(cq, newCell(cell.x+h, cell.y+h, h, polygon))
	}
	return Point{best.x, best.y}
}

// LargestPolygon picks the sub-polygon with the greatest net (holes-subtracted)
// shoelace area — DAWA runs polylabel on this one for a MultiPolygon.
func LargestPolygon(polys []Polygon) Polygon {
	var best Polygon
	bestArea := math.Inf(-1)
	for _, poly := range polys {
		if len(poly) == 0 {
			continue
		}
		area := math.Abs(ringArea(poly[0]))
		for _, hole := range poly[1:] {
			area -= math.Abs(ringArea(hole))
		}
		if area > bestArea {
			bestArea = area
			best = poly
		}
	}
	return best
}

type cell struct {
	x, y, h float64
	d       float64 // signed distance from cell centre to polygon
	max     float64 // max possible distance within the cell
}

func newCell(x, y, h float64, polygon Polygon) *cell {
	d := pointToPolygonDist(x, y, polygon)
	return &cell{x: x, y: y, h: h, d: d, max: d + h*sqrt2}
}

// getCentroidCell returns a zero-radius cell at the polygon's centroid.
func getCentroidCell(polygon Polygon) *cell {
	var area, x, y float64
	points := polygon[0]
	n := len(points)
	for i, j := 0, n-1; i < n; j, i = i, i+1 {
		a, b := points[i], points[j]
		f := a[0]*b[1] - b[0]*a[1]
		x += (a[0] + b[0]) * f
		y += (a[1] + b[1]) * f
		area += f * 3
	}
	if area == 0 {
		return newCell(points[0][0], points[0][1], 0, polygon)
	}
	return newCell(x/area, y/area, 0, polygon)
}

// pointToPolygonDist is the signed distance to the polygon outline (positive
// inside, negative outside).
func pointToPolygonDist(x, y float64, polygon Polygon) float64 {
	inside := false
	minDistSq := math.Inf(1)
	for _, ring := range polygon {
		n := len(ring)
		for i, j := 0, n-1; i < n; j, i = i, i+1 {
			a, b := ring[i], ring[j]
			if (a[1] > y) != (b[1] > y) &&
				x < (b[0]-a[0])*(y-a[1])/(b[1]-a[1])+a[0] {
				inside = !inside
			}
			minDistSq = math.Min(minDistSq, segDistSq(x, y, a, b))
		}
	}
	d := math.Sqrt(minDistSq)
	if inside {
		return d
	}
	return -d
}

// segDistSq is the squared distance from (px,py) to segment a-b.
func segDistSq(px, py float64, a, b Point) float64 {
	x, y := a[0], a[1]
	dx, dy := b[0]-x, b[1]-y
	if dx != 0 || dy != 0 {
		t := ((px-x)*dx + (py-y)*dy) / (dx*dx + dy*dy)
		if t > 1 {
			x, y = b[0], b[1]
		} else if t > 0 {
			x += dx * t
			y += dy * t
		}
	}
	dx, dy = px-x, py-y
	return dx*dx + dy*dy
}

// ringArea is the signed shoelace area of a ring.
func ringArea(ring Ring) float64 {
	var sum float64
	n := len(ring)
	for i, j := 0, n-1; i < n; j, i = i, i+1 {
		sum += (ring[j][0] - ring[i][0]) * (ring[i][1] + ring[j][1])
	}
	return sum / 2
}

// cellQueue is a max-heap of cells ordered by .max (largest potential first).
type cellQueue []*cell

func (q cellQueue) Len() int           { return len(q) }
func (q cellQueue) Less(i, j int) bool { return q[i].max > q[j].max }
func (q cellQueue) Swap(i, j int)      { q[i], q[j] = q[j], q[i] }
func (q *cellQueue) Push(x any)        { *q = append(*q, x.(*cell)) }
func (q *cellQueue) Pop() any {
	old := *q
	n := len(old)
	it := old[n-1]
	old[n-1] = nil
	*q = old[:n-1]
	return it
}
