// Package inventoryhttp exposes the inventory and "Necesita tu atención" over
// HTTP (docs/api.md §11).
package inventoryhttp

import (
	"bytes"
	"encoding/csv"
	"strconv"
	"strings"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/Abraxas-365/odyssey/internal/httpx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/inventory"
	"github.com/Abraxas-365/odyssey/internal/permission"
	"github.com/Abraxas-365/odyssey/internal/project"
	"github.com/gofiber/fiber/v2"
)

type Handler struct{ queries inventory.Queries }

func New(queries inventory.Queries) *Handler { return &Handler{queries: queries} }

// Register mounts the routes on an already-authenticated router.
func (h *Handler) Register(r fiber.Router) {
	r.Get("/inventory", httpx.RequirePermissions(permission.InventoryRead), h.list)
	// Everyone who reads projects has a list: a builder's is their own apps
	// and requests, TI's adds what they watch over (decided by the actor's
	// permissions, never by a parameter).
	r.Get("/attention", httpx.RequirePermissions(permission.ProjectsRead), h.attention)
}

func (h *Handler) attention(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	got, err := h.queries.Attention(c.UserContext(), org)
	if err != nil {
		return err
	}
	return c.JSON(got)
}

// list: ?tier= (repeatable), ?cluster=, ?needs_attention=true, ?search= (the
// app's name or slug), ?limit=&offset=; ?format=csv downloads every row that
// matches (no paging).
func (h *Handler) list(c *fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	var f inventory.Filter
	if f.Tier, err = httpx.EnumFilter(c, "tier", project.Tier.Valid, project.TierMessage); err != nil {
		return err
	}
	if raw := strings.TrimSpace(c.Query("cluster")); raw != "" {
		if f.Cluster, err = fleet.ParseClusterID(raw); err != nil {
			return err
		}
	}
	switch c.Query("needs_attention") {
	case "", "false":
	case "true":
		f.NeedsAttention = true
	default:
		return errx.InvalidField("needs_attention", "needs_attention must be true or false")
	}
	page, err := httpx.Pagination(c)
	if err != nil {
		return err
	}
	f.Search = page.Search
	switch format := c.Query("format"); format {
	case "", "json":
	case "csv":
		return h.csv(c, org, f)
	default:
		return errx.InvalidField("format", "format must be json or csv")
	}
	got, err := h.queries.List(c.UserContext(), org, f, page)
	if err != nil {
		return err
	}
	return c.JSON(got)
}

func (h *Handler) csv(c *fiber.Ctx, org identity.OrganizationID, f inventory.Filter) error {
	page, err := h.queries.Export(c.UserContext(), org, f)
	if err != nil {
		return err
	}
	body, err := CSV(page.Items)
	if err != nil {
		return err
	}
	c.Set(fiber.HeaderContentType, "text/csv; charset=utf-8")
	c.Set(fiber.HeaderContentDisposition, `attachment; filename="inventario.csv"`)
	c.Set("X-Inventory-Truncated", strconv.FormatBool(page.Truncated))
	// the sources the filter needed and that could not be read (the CSV is
	// then incomplete), comma separated; empty when all were read
	unavailable := make([]string, len(page.Unavailable))
	for i, s := range page.Unavailable {
		unavailable[i] = string(s)
	}
	c.Set("X-Inventory-Unavailable", strings.Join(unavailable, ","))
	return c.Send(body)
}

// Header is the first line of the CSV: plain Spanish, like the console.
var Header = []string{"App", "Entorno", "Dueño", "Nivel", "Acceso", "URL", "Estado", "Versión en línea", "En línea desde",
	"Recursos", "Usa IA", "Secretos", "Secretos por cargar", "Escaneo", "Críticas", "Altas", "Clúster", "Necesita atención"}

// CSV renders the rows. Every cell that a spreadsheet could read as a formula
// (it starts with = + - @, a tab or a carriage return) is prefixed with an
// apostrophe, so a hostile app name or secret key is text there, not a
// formula (OWASP CSV injection).
func CSV(rows []inventory.Row) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("\ufeff") // Excel reads UTF-8 only with a BOM
	w := csv.NewWriter(&buf)
	if err := w.Write(Header); err != nil {
		return nil, err
	}
	for _, r := range rows {
		resources := make([]string, len(r.Resources))
		for i, x := range r.Resources {
			resources[i] = x.Name + " (" + x.Status + ")"
		}
		cluster := ""
		if r.Cluster != nil {
			cluster = r.Cluster.Name
		}
		image, since := "", ""
		if r.Online != nil {
			image = r.Online.Image
			if r.Online.Since != nil {
				since = r.Online.Since.UTC().Format(time.RFC3339)
			}
		}
		cells := []string{r.App.Name, r.Environment.Slug, r.Owner.String(), string(r.App.Tier), string(r.Access), r.URL, string(r.Status), image, since,
			strings.Join(resources, "; "), yesNo(r.AI), strconv.Itoa(r.Secrets.Total), strings.Join(r.Secrets.Missing, "; "),
			string(r.Scan.State), strconv.Itoa(r.Scan.Critical), strconv.Itoa(r.Scan.High), cluster, yesNo(r.NeedsAttention)}
		for i, cell := range cells {
			cells[i] = Safe(cell)
		}
		if err := w.Write(cells); err != nil {
			return nil, err
		}
	}
	w.Flush()
	return buf.Bytes(), w.Error()
}

// Safe neutralizes a cell a spreadsheet would run as a formula.
func Safe(cell string) string {
	if cell != "" && strings.ContainsRune("=+-@\t\r", rune(cell[0])) {
		return "'" + cell
	}
	return cell
}

func yesNo(b bool) string {
	if b {
		return "sí"
	}
	return "no"
}
