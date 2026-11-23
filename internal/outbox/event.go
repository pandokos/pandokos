package outbox

import (
	"bytes"
	"encoding/json"
	"strconv"
	"time"

	"github.com/pandokos/pandokos/internal/tenant"
)

// Event is an event as the log holds it.
type Event struct {
	Seq     int64 // sequence number in the log of the tenant or of the platform; 0: none (a test event)
	ID      string
	Type    string
	Subject string
	Time    time.Time
	Data    json.RawMessage
}

// Envelope is what the rendering adds to an event: the source of the installation and, for an
// event of a tenant, the tenant and its external reference as they are when the document is
// rendered.
type Envelope struct {
	Source    string
	TenantID  tenant.ID // the zero value: no tenant attributes
	TenantRef string
}

// Render returns the CloudEvents 1.0 document of e (sprint 6.4, section 5.3): structured JSON, the
// members in a fixed order. A zero Seq leaves out the sequence attribute; an empty subject, a zero
// TenantID and an empty TenantRef leave out theirs. The data is written byte for byte.
func Render(e Event, env Envelope) []byte {
	var b bytes.Buffer
	b.Grow(320 + len(e.Data))
	b.WriteString(`{"specversion":"1.0","id":`)
	writeString(&b, e.ID)
	b.WriteString(`,"source":`)
	writeString(&b, env.Source)
	b.WriteString(`,"type":`)
	writeString(&b, e.Type)
	b.WriteString(`,"time":"`)
	b.WriteString(e.Time.UTC().Format("2006-01-02T15:04:05.000000Z"))
	b.WriteByte('"')
	if e.Subject != "" {
		b.WriteString(`,"subject":`)
		writeString(&b, e.Subject)
	}
	b.WriteString(`,"datacontenttype":"application/json"`)
	if env.TenantID != (tenant.ID{}) {
		b.WriteString(`,"tenantid":"` + env.TenantID.String() + `"`)
		if env.TenantRef != "" {
			b.WriteString(`,"tenantref":`)
			writeString(&b, env.TenantRef)
		}
	}
	if e.Seq != 0 {
		b.WriteString(`,"sequence":"` + strconv.FormatInt(e.Seq, 10) + `"`)
	}
	b.WriteString(`,"data":`)
	if len(e.Data) == 0 {
		b.WriteString("null")
	} else {
		b.Write(e.Data)
	}
	b.WriteByte('}')
	return b.Bytes()
}

// writeString writes s as a JSON string, without the escaping of HTML characters.
func writeString(b *bytes.Buffer, s string) {
	enc := json.NewEncoder(b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)       // a string always encodes
	b.Truncate(b.Len() - 1) // the newline of Encode
}
