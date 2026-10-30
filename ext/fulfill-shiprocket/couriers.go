package shiprocket

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/itswadesh/gocommerce/core"
)

// What an account can do before anything is booked: where it collects from,
// and which couriers will carry a parcel between two pincodes at what price.

// PickupLocation is one of the account's pickup addresses. Name is the
// nickname a booking names it by.
type PickupLocation struct {
	Name     string `json:"name"`
	Address  string `json:"address"`
	Address2 string `json:"address_2,omitempty"`
	City     string `json:"city"`
	State    string `json:"state"`
	Country  string `json:"country"`
	Pincode  string `json:"pincode"`
	Phone    string `json:"phone,omitempty"`
	Email    string `json:"email,omitempty"`
	Contact  string `json:"contact,omitempty"`
	Primary  bool   `json:"primary"`
}

// pickupLocations lists them. "Pickup Addresses › Get All Pickup Locations":
// GET /v1/external/settings/company/pickup → data.shipping_address[].
func (m *Module) pickupLocations(ctx context.Context, c *Config) ([]PickupLocation, error) {
	var out struct {
		Data struct {
			ShippingAddress []struct {
				PickupLocation string `json:"pickup_location"`
				Address        string `json:"address"`
				Address2       string `json:"address_2"`
				City           string `json:"city"`
				State          string `json:"state"`
				Country        string `json:"country"`
				PinCode        string `json:"pin_code"`
				Phone          string `json:"phone"`
				Email          string `json:"email"`
				Name           string `json:"name"`
				Primary        number `json:"is_primary_location"`
			} `json:"shipping_address"`
		} `json:"data"`
	}
	if err := m.callAs(ctx, c, http.MethodGet, "/v1/external/settings/company/pickup", nil, nil, &out); err != nil {
		return nil, err
	}
	list := make([]PickupLocation, 0, len(out.Data.ShippingAddress))
	for _, a := range out.Data.ShippingAddress {
		list = append(list, PickupLocation{
			Name: a.PickupLocation, Address: a.Address, Address2: a.Address2, City: a.City,
			State: a.State, Country: a.Country, Pincode: a.PinCode, Phone: a.Phone,
			Email: a.Email, Contact: a.Name, Primary: a.Primary == 1,
		})
	}
	return list, nil
}

// pickup is the configured pickup location's address, which serviceability
// needs the pincode of and a return needs as its destination.
func (m *Module) pickup(ctx context.Context) (PickupLocation, error) {
	name := strings.TrimSpace(m.conf(ctx).PickupLocation)
	key := cacheKey(ctx, "pickup", credentialKey(m.conf(ctx)), name)
	if v, ok := m.cache.get(key); ok {
		return v.(PickupLocation), nil
	}
	all, err := m.pickupLocations(ctx, m.conf(ctx))
	if err != nil {
		return PickupLocation{}, err
	}
	for _, p := range all {
		if strings.EqualFold(strings.TrimSpace(p.Name), name) {
			m.cache.put(key, p, 10*time.Minute)
			return p, nil
		}
	}
	return PickupLocation{}, gocommerce.Validationf(
		"Shiprocket has no pickup location called %q on this account. Pick one under Settings › Shipping providers › Shiprocket", name)
}

// Courier is one courier willing to carry a parcel, as Shiprocket quoted it.
type Courier struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	// Rate is Shiprocket's price for this parcel, in paise.
	Rate gocommerce.Money `json:"rate"`
	COD  bool             `json:"cod"`
	// EstimatedDays and ETD are Shiprocket's estimate, as it gave it. Absent
	// when it gave none: an estimate is never made up here.
	EstimatedDays int     `json:"estimated_days,omitempty"`
	ETD           string  `json:"etd,omitempty"`
	Rating        float64 `json:"rating,omitempty"`
}

// Quote is the couriers for one parcel to one pincode, with the three picks an
// operator usually wants named.
type Quote struct {
	PickupPostcode   string    `json:"pickup_postcode"`
	DeliveryPostcode string    `json:"delivery_postcode"`
	COD              bool      `json:"cod"`
	WeightKg         float64   `json:"weight_kg"`
	Couriers         []Courier `json:"couriers"`
	Recommended      int       `json:"recommended_id,omitempty"`
	Cheapest         int       `json:"cheapest_id,omitempty"`
	Fastest          int       `json:"fastest_id,omitempty"`
}

// parcelSpec is what a quote depends on.
type parcelSpec struct {
	deliveryPin         string
	cod                 bool
	weightKg            float64
	lengthCm, breadthCm float64
	heightCm            float64
	declaredMinor       int64
}

// quote asks which couriers serve a parcel. "Couriers › Check Courier
// Serviceability": GET /v1/external/courier/serviceability/ with
// pickup_postcode, delivery_postcode, cod, weight (kg), length, breadth,
// height (cm) and declared_value (rupees) → data.available_courier_companies[]
// and data.recommended_courier_company_id.
//
// Answers are kept ten minutes: the panel asks when the ship dialog opens and
// again when a weight changes, and a storefront asks per pincode, and none of
// those should be a call to Shiprocket each time.
func (m *Module) quote(ctx context.Context, p parcelSpec) (*Quote, error) {
	from, err := m.pickup(ctx)
	if err != nil {
		return nil, err
	}
	cod := "0"
	if p.cod {
		cod = "1"
	}
	q := url.Values{
		"pickup_postcode":   {from.Pincode},
		"delivery_postcode": {p.deliveryPin},
		"cod":               {cod},
		"weight":            {strconv.FormatFloat(p.weightKg, 'f', -1, 64)},
	}
	if p.lengthCm > 0 && p.breadthCm > 0 && p.heightCm > 0 {
		q.Set("length", strconv.FormatFloat(p.lengthCm, 'f', -1, 64))
		q.Set("breadth", strconv.FormatFloat(p.breadthCm, 'f', -1, 64))
		q.Set("height", strconv.FormatFloat(p.heightCm, 'f', -1, 64))
	}
	if p.declaredMinor > 0 {
		q.Set("declared_value", string(rupees(p.declaredMinor)))
	}
	key := cacheKey(ctx, "quote", credentialKey(m.conf(ctx)), q.Encode())
	if v, ok := m.cache.get(key); ok {
		return v.(*Quote), nil
	}

	var out struct {
		Status  int    `json:"status"`
		Message string `json:"message"`
		Data    struct {
			Available []struct {
				ID            int    `json:"courier_company_id"`
				Name          string `json:"courier_name"`
				Rate          number `json:"rate"`
				COD           number `json:"cod"`
				EstimatedDays number `json:"estimated_delivery_days"`
				ETD           string `json:"etd"`
				Rating        number `json:"rating"`
				Blocked       number `json:"blocked"`
			} `json:"available_courier_companies"`
			Recommended int `json:"recommended_courier_company_id"`
		} `json:"data"`
	}
	if err := m.call(ctx, http.MethodGet, "/v1/external/courier/serviceability/", q, nil, &out); err != nil {
		return nil, err
	}
	// A refusal can come back as HTTP 200 with its own status in the body.
	if out.Status != 0 && out.Status != http.StatusOK && len(out.Data.Available) == 0 {
		return nil, refused(firstNonEmpty(out.Message, fmt.Sprintf("status %d", out.Status)))
	}
	res := &Quote{PickupPostcode: from.Pincode, DeliveryPostcode: p.deliveryPin, COD: p.cod, WeightKg: p.weightKg, Couriers: []Courier{}}
	for _, c := range out.Data.Available {
		if c.Blocked == 1 || (p.cod && c.COD != 1) {
			// A courier that cannot collect cash is no answer for a
			// cash-on-delivery parcel, however cheap.
			continue
		}
		res.Couriers = append(res.Couriers, Courier{
			ID: c.ID, Name: c.Name, COD: c.COD == 1,
			Rate:          gocommerce.Money{AmountMinor: c.Rate.minor(), Currency: "INR"},
			EstimatedDays: int(c.EstimatedDays), ETD: strings.TrimSpace(c.ETD), Rating: float64(c.Rating),
		})
	}
	sort.SliceStable(res.Couriers, func(i, j int) bool {
		return res.Couriers[i].Rate.AmountMinor < res.Couriers[j].Rate.AmountMinor
	})
	if len(res.Couriers) > 0 {
		res.Cheapest = res.Couriers[0].ID
		fastest := res.Couriers[0]
		for _, c := range res.Couriers {
			if c.EstimatedDays > 0 && (fastest.EstimatedDays == 0 || c.EstimatedDays < fastest.EstimatedDays) {
				fastest = c
			}
		}
		res.Fastest = fastest.ID
		for _, c := range res.Couriers {
			if c.ID == out.Data.Recommended {
				res.Recommended = c.ID
			}
		}
	}
	m.cache.put(key, res, 10*time.Minute)
	return res, nil
}

// ------------------------------------------------------------------ cache

// cache is a small expiring map. ponytail: one map per process with a hard
// size cap that empties it when reached; an LRU if quotes ever crowd it.
type cache struct {
	mu   sync.Mutex
	vals map[string]cached
}

type cached struct {
	v   any
	exp time.Time
}

const cacheCap = 5000

func (c *cache) get(k string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.vals[k]
	if !ok || time.Now().After(e.exp) {
		return nil, false
	}
	return e.v, true
}

func (c *cache) put(k string, v any, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.vals == nil || len(c.vals) >= cacheCap {
		c.vals = map[string]cached{}
	}
	c.vals[k] = cached{v: v, exp: time.Now().Add(ttl)}
}

// cacheKey keeps one store's answers from ever serving another: two stores on
// different accounts may see different couriers and different rates.
func cacheKey(ctx context.Context, parts ...string) string {
	store, _ := gocommerce.StoreFrom(ctx)
	return store + "\x00" + strings.Join(parts, "\x00")
}
