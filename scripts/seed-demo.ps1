# Fills a dev store with enough data to design against.
#
#   .\scripts\seed-demo.ps1 -BaseUrl http://127.0.0.1:8090 -Token dev-token
#
# `seed.ps1` makes four products, which is the right size for the smoke test and
# far too small to lay out a screen: a table with four rows never paginates, a
# sales chart with one day of orders is a single bar, and "running low" is empty
# because nothing is low. This makes a store with a few hundred orders spread
# over months, so the panel can be judged on what it will actually hold.
#
# Everything here goes through the API — no SQL. Historical orders are the one
# thing the ordinary POST cannot express, because the service stamps
# `created_at` itself; POST /api/admin/import/orders exists for migrations from
# other platforms and takes a `created_at` column, so the history is loaded
# through that rather than by writing the table. It also deliberately fires no
# events and moves no stock, which is what we want: this is history, not two
# hundred confirmation emails.
#
# Safe to re-run. Products are matched by slug, locations and categories by
# code/slug, and the importer skips an order number it already has.

param(
    [string]$BaseUrl = $env:GC_BASE,
    [string]$Token   = $env:GC_TOKEN,
    [int]$Orders     = 260,
    [int]$Days       = 150,
    [int]$Carts      = 24,
    [switch]$Reset
)

$ErrorActionPreference = 'Stop'
if ($BaseUrl) { $env:GC_BASE = $BaseUrl }
if ($Token)   { $env:GC_TOKEN = $Token }
. "$PSScriptRoot\gc.ps1"

Write-Host "Seeding demo data into $(Get-GCBase)" -ForegroundColor Cyan

# A fixed seed so two runs of this script produce the same store. A demo whose
# numbers move every time is one nobody can screenshot twice.
$rand = [System.Random]::new(20260913)
function Pick { param($Items) $Items[$rand.Next(0, $Items.Count)] }
function Between { param([int]$Lo, [int]$Hi) $rand.Next($Lo, $Hi + 1) }

# --------------------------------------------------------------- locations

Write-GCStep 'Locations'
$locations = @(
    @{ code = 'MAIN';  name = 'Main warehouse';   priority = 10; city = 'Rotterdam';  country = 'NL' },
    @{ code = 'BER';   name = 'Berlin stockroom'; priority = 20; city = 'Berlin';     country = 'DE' },
    @{ code = 'LON';   name = 'London shop';      priority = 30; city = 'London';     country = 'GB' },
    @{ code = 'NYC';   name = 'Brooklyn studio';  priority = 40; city = 'New York';   country = 'US' },
    @{ code = 'OVER';  name = 'Overflow unit';    priority = 90; city = 'Rotterdam';  country = 'NL' }
)
$existingLocations = @{}
foreach ($l in (Invoke-GC GET '/api/admin/locations' -Admin)) { $existingLocations[$l.code] = $l }
foreach ($l in $locations) {
    if ($existingLocations.ContainsKey($l.code)) { Write-Host "  skip  $($l.code)"; continue }
    $body = @{
        code = $l.code; name = $l.name; priority = $l.priority; active = $true
        address = @{ city = $l.city; country = $l.country }
    }
    $made = Invoke-GC POST '/api/admin/locations' $body -Admin
    $existingLocations[$l.code] = $made
    Write-Host ("  new   {0}  {1}" -f $l.code, $l.name) -ForegroundColor DarkGreen
}

# -------------------------------------------------------------- categories
#
# The store already ships a standard product taxonomy of a few hundred
# categories, so this invents nothing: it maps the product families below onto
# slugs that are already there. Two things made that the right call. A parallel
# "apparel / tops / knitwear" tree beside the real "clothing" one is two answers
# to the same question, and the listing pages at 200 — a naive existence check
# reads the first page, misses a slug sitting on the second, and then fails on
# the conflict when it tries to create it.

Write-GCStep 'Categories'

# Two levels, because the categories screen draws a tree and a flat list gives
# it nothing to draw. It indents by depth and puts an expander on any row with
# children; eight roots with no parents between them render as a plain table and
# the whole feature is invisible. Four roots with two or three leaves each is
# enough to see it work without inventing a taxonomy nobody asked for.
#
# Looked up one slug at a time rather than listed. A store that has had the
# standard taxonomy imported holds 14,606 categories, and
# `/api/admin/categories` ignores `limit` and `offset` (meta comes back
# `offset: 0, page: 1` whatever you ask for), so a loop that pages until it sees
# a short page never sees one and spins on the same 200 rows forever.
$categoryTree = [ordered]@{
    'apparel'     = @{ title = 'Apparel';        children = [ordered]@{ 'clothing' = 'Clothing'; 'clothing-accessories' = 'Clothing accessories' } }
    'home-living' = @{ title = 'Home and living'; children = [ordered]@{ 'kitchen-dining' = 'Kitchen and dining'; 'decor' = 'Decor'; 'linens-bedding' = 'Linens and bedding' } }
    'bags'        = @{ title = 'Bags';           children = [ordered]@{ 'tote-bags' = 'Tote bags'; 'backpacks' = 'Backpacks' } }
    'workspace'   = @{ title = 'Workspace';      children = [ordered]@{ 'office-supplies' = 'Office supplies' } }
}

function Resolve-Category {
    param([string]$Slug, [string]$Title, $ParentID)
    try {
        $found = Invoke-GC GET "/api/categories/$Slug"
        # It may already exist from an earlier run as a root. Re-parent it
        # rather than leaving the tree half-built.
        if ($ParentID -and $found.parent_id -ne $ParentID) {
            Invoke-GC PATCH "/api/admin/categories/$($found.id)" @{ parent_id = $ParentID } -Admin | Out-Null
            Write-Host ("  moved {0} under its parent" -f $Slug) -ForegroundColor DarkGreen
        }
        return $found.id
    } catch {
        # Not found is the ordinary case on a store without the taxonomy.
    }
    $body = @{ slug = $Slug; title = $Title }
    if ($ParentID) { $body.parent_id = $ParentID }
    $made = Invoke-GC POST '/api/admin/categories' $body -Admin
    Write-Host ("  new   {0}" -f $Slug) -ForegroundColor DarkGreen
    return $made.id
}

$categoryIds = @{}
foreach ($rootSlug in $categoryTree.Keys) {
    $rootId = Resolve-Category -Slug $rootSlug -Title $categoryTree[$rootSlug].title -ParentID $null
    $categoryIds[$rootSlug] = $rootId
    foreach ($childSlug in $categoryTree[$rootSlug].children.Keys) {
        $categoryIds[$childSlug] = Resolve-Category -Slug $childSlug `
            -Title $categoryTree[$rootSlug].children[$childSlug] -ParentID $rootId
    }
}
Write-Host ("  {0} categories across {1} roots" -f $categoryIds.Count, $categoryTree.Count)

# ------------------------------------------------------------- collections

Write-GCStep 'Collections'
$collections = @(
    @{ slug = 'new-in';      title = 'New in';          description = 'Everything added in the last month.' },
    @{ slug = 'best-of';     title = 'Best of the year'; description = 'What sold most since January.' },
    @{ slug = 'winter';      title = 'Winter';          description = 'Warm things, for the season.' },
    @{ slug = 'gifts';       title = 'Gifts under 30';  description = 'Small enough to post.' },
    @{ slug = 'last-chance'; title = 'Last chance';     description = 'Not being restocked.' }
)
$collectionIds = @{}
foreach ($c in (Invoke-GC GET '/api/admin/collections?limit=100' -Admin)) { $collectionIds[$c.slug] = $c.id }
foreach ($c in $collections) {
    if ($collectionIds.ContainsKey($c.slug)) { Write-Host "  skip  $($c.slug)"; continue }
    $made = Invoke-GC POST '/api/admin/collections' $c -Admin
    $collectionIds[$c.slug] = $made.id
    Write-Host ("  new   {0}" -f $c.slug) -ForegroundColor DarkGreen
}

# ---------------------------------------------------------------- products

Write-GCStep 'Products'

# Built from parts rather than written out: sixty hand-written products is a lot
# of prose to maintain and reads no better than this does.
$families = @(
    @{ noun = 'tee';      cat = 'clothing';             prefix = 'TEE'; lo = 1800; hi = 3200; sized = $true  },
    @{ noun = 'hoodie';   cat = 'clothing';             prefix = 'HOD'; lo = 4500; hi = 7500; sized = $true  },
    @{ noun = 'shirt';    cat = 'clothing';             prefix = 'SHT'; lo = 3500; hi = 5900; sized = $true  },
    @{ noun = 'jumper';   cat = 'clothing';             prefix = 'JMP'; lo = 5500; hi = 9500; sized = $true  },
    @{ noun = 'scarf';    cat = 'clothing-accessories'; prefix = 'SCF'; lo = 2200; hi = 4800; sized = $false },
    @{ noun = 'cap';      cat = 'clothing-accessories'; prefix = 'CAP'; lo = 1800; hi = 2900; sized = $false },
    @{ noun = 'socks';    cat = 'clothing-accessories'; prefix = 'SCK'; lo = 900;  hi = 1800; sized = $true  },
    @{ noun = 'mug';      cat = 'kitchen-dining';       prefix = 'MUG'; lo = 1200; hi = 2400; sized = $false },
    @{ noun = 'bottle';   cat = 'kitchen-dining';       prefix = 'BTL'; lo = 1900; hi = 3600; sized = $false },
    @{ noun = 'tumbler';  cat = 'kitchen-dining';       prefix = 'TMB'; lo = 2200; hi = 3900; sized = $false },
    @{ noun = 'notebook'; cat = 'office-supplies';      prefix = 'NTB'; lo = 800;  hi = 2200; sized = $false },
    @{ noun = 'pen';      cat = 'office-supplies';      prefix = 'PEN'; lo = 400;  hi = 1600; sized = $false },
    @{ noun = 'candle';   cat = 'decor';                prefix = 'CDL'; lo = 1400; hi = 3400; sized = $false },
    @{ noun = 'blanket';  cat = 'linens-bedding';       prefix = 'BLK'; lo = 3900; hi = 8900; sized = $false },
    @{ noun = 'tote';     cat = 'tote-bags';            prefix = 'TOT'; lo = 1500; hi = 3200; sized = $false },
    @{ noun = 'backpack'; cat = 'backpacks';            prefix = 'BPK'; lo = 4900; hi = 9900; sized = $false }
)
$materials = @('Cotton', 'Merino', 'Linen', 'Enamel', 'Canvas', 'Recycled', 'Oak', 'Copper', 'Stoneware', 'Waxed')
$qualifiers = @('everyday', 'weekend', 'studio', 'field', 'harbour', 'garden', 'atlas', 'meridian', 'northerly', 'quiet')
$sizes = @('S', 'M', 'L', 'XL')
$colours = @('Black', 'Ecru', 'Navy', 'Moss', 'Rust')

$existingSlugs = @{}
foreach ($p in (Invoke-GC GET '/api/admin/products?limit=200' -Admin)) { $existingSlugs[$p.slug] = $true }

$madeProducts = 0
foreach ($family in $families) {
    foreach ($qualifier in ($qualifiers | Select-Object -First 4)) {
        $material = Pick $materials
        $title = "$material $qualifier $($family.noun)"
        $slug = ($title -replace '[^a-zA-Z0-9]+', '-').ToLower().Trim('-')
        if ($existingSlugs.ContainsKey($slug)) { continue }

        $base = Between $family.lo $family.hi
        $base = [int][math]::Round($base / 100) * 100 - 1   # 2499 rather than 2500
        $stem = "$($family.prefix)-$($qualifier.Substring(0,3).ToUpper())"

        # One in nine is a draft, and one in eleven archived, so the status
        # filter on the products screen has something to filter.
        $status = 'active'
        if ($madeProducts % 9 -eq 4)  { $status = 'draft' }
        if ($madeProducts % 11 -eq 7) { $status = 'archived' }

        if (-not $categoryIds.ContainsKey($family.cat)) {
            throw "the taxonomy has no category '$($family.cat)'. Import the taxonomy first."
        }
        $body = @{
            slug = $slug; title = $title; status = $status
            description = "A $($family.noun) in $($material.ToLower()). Part of the $qualifier range."
            category_id = $categoryIds[$family.cat]
        }

        if ($family.sized) {
            $variants = @()
            foreach ($size in $sizes) {
                $colour = Pick $colours
                # A deliberate spread: some sold out, some down to the last few
                # so "running low" is populated, most comfortable.
                $stock = Between 0 40
                if ($madeProducts % 7 -eq 0 -and $size -eq 'XL') { $stock = 0 }
                if ($madeProducts % 5 -eq 0 -and $size -eq 'S')  { $stock = Between 1 4 }
                $variants += @{
                    sku = "$stem-$size"; price_minor = $base; options = @($size, $colour)
                    stock_on_hand = $stock; weight_grams = (Between 120 900)
                }
            }
            $body.options = @(
                @{ name = 'Size';   values = $sizes },
                @{ name = 'Colour'; values = $colours }
            )
            $body.variants = $variants
        } else {
            $body.sku = "$stem-001"
            $body.price_minor = $base
            $body.stock = Between 0 60
            if ($madeProducts % 6 -eq 3) { $body.stock = Between 1 5 }
        }

        try {
            $made = Invoke-GC POST '/api/admin/products' $body -Admin
        } catch {
            if ("$_" -match 'conflict') { continue }
            throw
        }
        $existingSlugs[$slug] = $true
        $madeProducts++

        # A product in no collection makes the collections screen a list of
        # empty shelves, so each one joins one or two.
        if ($collectionIds.Count -gt 0) {
            $slugsForThis = @((Pick @('new-in', 'best-of', 'winter', 'gifts', 'last-chance')))
            if ($madeProducts % 3 -eq 0) { $slugsForThis += (Pick @('new-in', 'gifts')) }
            $ids = @($slugsForThis | Select-Object -Unique | ForEach-Object { $collectionIds[$_] })
            try {
                Invoke-GC PUT "/api/admin/products/$($made.id)/collections" @{ collection_ids = $ids } -Admin | Out-Null
            } catch {
                Write-Host "  could not set collections on $slug" -ForegroundColor Yellow
            }
        }

        if ($madeProducts % 10 -eq 0) { Write-Host "  ...$madeProducts products" }
    }
}
Write-Host ("  {0} product(s) created" -f $madeProducts) -ForegroundColor DarkGreen

# The order history is built from what the store actually sells, not from what
# this run happened to create. Reading it back is what makes a second run useful
# rather than a no-op: the catalog is already there, so it lists it and gets on
# with the orders. Only active products qualify — a draft that had been selling
# would be a contradiction on the reports screen.
$skuPool = @()
foreach ($p in (Invoke-GC GET '/api/admin/products?limit=200&status=active' -Admin)) {
    foreach ($v in $p.variants) {
        if (-not $v.active) { continue }
        $skuPool += @{ sku = $v.sku; price = $v.price.amount_minor; title = $p.title; label = $v.label }
    }
}
Write-Host ("  {0} sellable sku(s) across the active catalog" -f $skuPool.Count) -ForegroundColor DarkGreen

if ($skuPool.Count -eq 0) {
    throw 'no active variants to sell, so there is no history to build'
}

# ------------------------------------------------------------ stock spread
#
# Creating a product puts all of its stock at the default location, so a store
# with five warehouses shows one holding everything and four holding nothing —
# which makes the locations screen a column of zeros and the fill-order rule it
# describes ("the first one, top to bottom, that can cover the line") impossible
# to see working. This spreads it.
#
# Transfers go through the API, one statement out and one in inside a single
# transaction, so the store's total never moves. Reserved units are left where
# they are: they are promised to orders that will be picked from that shelf.

Write-GCStep 'Spreading stock across locations'

$allLocations = Invoke-GC GET '/api/admin/locations' -Admin
$defaultLocation = $allLocations | Where-Object { $_.is_default } | Select-Object -First 1
$targets = @($allLocations | Where-Object { -not $_.is_default -and $_.active })

if (-not $defaultLocation -or $targets.Count -eq 0) {
    Write-Host '  nothing to spread: need a default and at least one other open location' -ForegroundColor Yellow
} else {
    $moved = 0
    $movedUnits = 0
    foreach ($p in (Invoke-GC GET '/api/admin/products?limit=200&status=active' -Admin)) {
        foreach ($v in $p.variants) {
            if (-not $v.track_inventory) { continue }
            $available = $v.stock_on_hand - $v.stock_reserved
            if ($available -lt 6) { continue }

            # Leave roughly half at the default and scatter the rest, so the
            # default still reads as the main site rather than as one of five.
            $toMove = [int][math]::Floor($available / 2)
            foreach ($t in ($targets | Get-Random -Count ([math]::Min(2, $targets.Count)))) {
                if ($toMove -lt 2) { break }
                $share = Between 1 $toMove
                try {
                    Invoke-GC POST "/api/admin/variants/$($v.id)/stock/transfer" @{
                        from_location_id = $defaultLocation.id
                        to_location_id   = $t.id
                        quantity         = $share
                    } -Admin | Out-Null
                    $toMove -= $share
                    $movedUnits += $share
                    $moved++
                } catch {
                    # A variant whose stock moved since the listing was read is
                    # not worth failing the whole seed over.
                    Write-Host "  could not move $($v.sku) to $($t.code)" -ForegroundColor Yellow
                }
            }
        }
        if ($moved -gt 0 -and $moved % 40 -eq 0) { Write-Host "  ...$moved transfers" }
    }
    Write-Host ("  {0} transfer(s) moving {1} unit(s) off {2}" -f
        $moved, $movedUnits, $defaultLocation.name) -ForegroundColor DarkGreen
}

# --------------------------------------------------------------- discounts

Write-GCStep 'Discounts'
$discounts = @(
    @{ code = 'WELCOME10'; title = 'Welcome 10%';    kind = 'percentage'; value_bp = 1000; scope = 'order' },
    @{ code = 'SPRING15';  title = 'Spring 15%';     kind = 'percentage'; value_bp = 1500; scope = 'order' },
    @{ code = 'FIVER';     title = '5 off';          kind = 'fixed';   value_minor = 500; scope = 'order' },
    @{ code = 'BULK20';    title = 'Bulk 20%';       kind = 'percentage'; value_bp = 2000; scope = 'order'; min_subtotal_minor = 10000 },
    @{ code = 'POSTFREE';  title = 'Free delivery';  kind = 'free_shipping'; scope = 'order' }
)
foreach ($d in $discounts) {
    try {
        Invoke-GC POST '/api/admin/discounts' $d -Admin | Out-Null
        Write-Host ("  new   {0}" -f $d.code) -ForegroundColor DarkGreen
    } catch {
        if ("$_" -match 'conflict|already') { Write-Host "  skip  $($d.code)" } else { throw }
    }
}

# --------------------------------------------------------------- shipping
#
# One zone and the nine rates a US store actually offers, which is more
# interesting to lay out than it sounds: three carrier services, each priced
# over disjoint bands of the basket subtotal, so one method name appears
# several times at different prices and the paid tier disappears once the
# basket is big enough to earn free carriage.
#
# The bands are half-open, [min, max), which is what lets two of them meet at a
# number without both claiming it. The engine refuses overlapping bands for one
# method name — a basket offered the same service twice at two prices has no
# answer to "how much is postage" — so "above $100" is a floor of 10000 and
# "below $100" a ceiling of the same 10000, and they do not collide.
#
# Prices are minor units, like every other amount the API takes. A rate with no
# ceiling omits the key rather than sending null: both mean "no ceiling" to the
# engine, and the absent one cannot be misread as zero.

Write-GCStep 'Shipping zones'

$zones = @(
    @{
        name      = 'Local'
        countries = @('US')
        states    = @()
        rates     = @(
            @{ name = 'USPS priority';         price_minor = 0;    min = 10000 }
            @{ name = 'USPS ground advantage'; price_minor = 0;    min = 0;     max = 10000 }
            @{ name = 'USPS priority';         price_minor = 700;  min = 0;     max = 10000 }
            @{ name = 'Fedex Overnight';       price_minor = 2000; min = 50000; max = 100000 }
            @{ name = 'Fedex Overnight';       price_minor = 3000; min = 10000; max = 50000 }
            @{ name = 'FedEx 2-Day';           price_minor = 700;  min = 0;     max = 10000 }
            @{ name = 'FedEx 2-Day';           price_minor = 0;    min = 10000; max = 100000 }
            @{ name = 'Fedex Overnight';       price_minor = 4500; min = 0;     max = 10000 }
            @{ name = 'Fedex Overnight';       price_minor = 0;    min = 100000 }
        )
    }
)

# The listing answers `{zone, rates}` pairs rather than zones carrying a rates
# key, because a zone with no prices is not something an operator can use and
# showing the two apart invites reading half the configuration. Both halves are
# wanted here, so both are kept.
$existing = @{}
foreach ($row in (Invoke-GC GET '/api/admin/shipping/zones' -Admin)) {
    $existing[$row.zone.name] = $row
}

foreach ($zone in $zones) {
    $row = $existing[$zone.name]
    $haveRates = @()
    if ($row) {
        $zoneId = $row.zone.id
        if ($row.rates) { $haveRates = $row.rates }
        Write-Host ("  zone  {0} (already there)" -f $zone.name)
    } else {
        # POST answers the zone itself, not a {zone, rates} pair.
        $created = Invoke-GC POST '/api/admin/shipping/zones' @{
            name      = $zone.name
            countries = $zone.countries
            states    = $zone.states
        } -Admin
        $zoneId = $created.id
        Write-Host ("  zone  {0} ({1})" -f $zone.name, ($zone.countries -join ', ')) -ForegroundColor DarkGreen
    }

    # A rate is identified by its name *and* its band, because the same service
    # legitimately appears several times in one zone. Comparing both is what
    # makes a re-run quiet instead of a row of refusals from the overlap guard.
    $seen = @{}
    foreach ($r in $haveRates) {
        $ceiling = 'none'
        if ($null -ne $r.max_subtotal_minor) { $ceiling = $r.max_subtotal_minor }
        $seen["$($r.name.ToLower())|$($r.min_subtotal_minor)|$ceiling"] = $true
    }

    $made = 0
    $kept = 0
    $position = 0
    foreach ($rate in $zone.rates) {
        $body = @{
            zone_id            = $zoneId
            name               = $rate.name
            price_minor        = $rate.price_minor
            min_subtotal_minor = $rate.min
            position           = $position
        }
        $ceiling = 'none'
        if ($rate.ContainsKey('max')) {
            $body.max_subtotal_minor = $rate.max
            $ceiling = $rate.max
        }
        $position++

        if ($seen["$($rate.name.ToLower())|$($rate.min)|$ceiling"]) { $kept++; continue }

        try {
            Invoke-GC POST '/api/admin/shipping/rates' $body -Admin | Out-Null
            $made++
        } catch {
            # An overlap is worth seeing rather than swallowing: it means the
            # bands in this file stopped being disjoint, which is a mistake
            # here and not a condition of the store.
            Write-Host ("  rate  {0} refused: {1}" -f $rate.name, "$_".Trim()) -ForegroundColor Yellow
        }
    }
    Write-Host ("  {0} rate(s) added, {1} already there" -f $made, $kept) -ForegroundColor DarkGreen
}

# ------------------------------------------------------------------ orders
#
# Written as one CSV and imported in a single request. The alternative — a POST
# per order — is several hundred round trips and still cannot say when the
# order was placed, which is the whole point of generating history.

Write-GCStep "Orders ($Orders over $Days days)"

$people = @(
    'Alice Brenner', 'Bo Halvorsen', 'Chen Wei', 'Dara Okonkwo', 'Elif Demir',
    'Finn Gallagher', 'Greta Lindqvist', 'Hana Suzuki', 'Ivan Petrov', 'Jorge Salas',
    'Kira Novak', 'Luca Ferrari', 'Maya Patel', 'Nils Andersen', 'Olga Ivanova',
    'Pedro Alves', 'Rosa Marquez', 'Sam Whitfield', 'Tomas Novotny', 'Yara Haddad'
)
$cities = @(
    @{ city = 'Amsterdam'; country = 'NL'; postal = '1011AB' },
    @{ city = 'Berlin';    country = 'DE'; postal = '10115'  },
    @{ city = 'Paris';     country = 'FR'; postal = '75001'  },
    @{ city = 'Madrid';    country = 'ES'; postal = '28001'  },
    @{ city = 'Dublin';    country = 'IE'; postal = 'D01'    },
    @{ city = 'Lisbon';    country = 'PT'; postal = '1100'   }
)

$rows = New-Object System.Collections.Generic.List[string]
$rows.Add('number,created_at,email,name,status,payment_status,payment_provider,address_line1,city,postal_code,country,shipping_minor,discount_minor,sku,title,variant_label,quantity,unit_price_minor')

# Start well clear of anything the store already has, so re-running never
# collides with a number the checkout flow issued.
$seq = 5000
$now = (Get-Date).ToUniversalTime()

for ($i = 0; $i -lt $Orders; $i++) {
    $seq++
    $number = 'GC-D{0:D6}' -f $seq

    # Spread over the window with a bias toward recent weeks, so the chart has a
    # shape rather than a flat field of identical bars.
    $skew = [math]::Pow($rand.NextDouble(), 1.6)
    $daysAgo = [int][math]::Floor($skew * $Days)
    $placed = $now.AddDays(-$daysAgo).AddHours(-(Between 0 20)).AddMinutes(-(Between 0 59))
    $createdAt = $placed.ToString('yyyy-MM-ddTHH:mm:ssZ')

    $person = Pick $people
    $handle = ($person -replace '[^a-zA-Z]', '').ToLower()
    $email = "$handle@example.com"
    $place = Pick $cities

    # Older orders have had time to arrive; recent ones are still moving. A
    # store where everything is "delivered" cannot exercise the fulfilment
    # filters, and one where nothing is cannot exercise the reports.
    # Only statuses the import can honestly back up. `shipped` and `partial`
    # are deliberately absent: the importer writes no fulfillment rows, and
    # `gocommerce doctor`'s fulfillment check reads exactly those two against
    # the parcels — an order claiming to be half-shipped with nothing in
    # fulfillment_lines is drift, and seeding drift into a demo store means the
    # first thing anyone runs reports a problem we invented. `delivered` is
    # outside that check's scope, so history can still arrive.
    if ($daysAgo -gt 21) {
        $status = Pick @('delivered', 'delivered', 'delivered', 'delivered', 'cancelled')
    } elseif ($daysAgo -gt 7) {
        $status = Pick @('delivered', 'delivered', 'delivered', 'confirmed', 'cancelled')
    } else {
        $status = Pick @('confirmed', 'confirmed', 'confirmed', 'pending')
    }

    # And no `refunded` here either. The importer sets refunded_minor from the
    # payment status but writes no order_refunds row, so the doctor's refund
    # check — refunded_minor against the sum of succeeded refunds — fails on
    # every one. Refunds are issued further down through the API instead, which
    # writes the record the check is looking for.
    if ($status -eq 'cancelled' -or $status -eq 'pending') {
        $payment = 'pending'
    } else {
        $payment = 'paid'
    }
    $provider = Pick @('card', 'card', 'card', 'cod', 'ideal')

    $shipping = Pick @(0, 0, 399, 499, 699)
    $discount = 0
    if ($i % 9 -eq 0) { $discount = Pick @(500, 750, 1000) }

    $lineCount = Between 1 4
    for ($n = 0; $n -lt $lineCount; $n++) {
        $item = Pick $skuPool
        $qty = Pick @(1, 1, 1, 2, 2, 3)
        # Only the first row of a group carries the order's own columns; the
        # importer reads them from the first row it sees for that number.
        if ($n -eq 0) {
            $rows.Add(('{0},{1},{2},{3},{4},{5},{6},{7},{8},{9},{10},{11},{12},{13},{14},{15},{16},{17}' -f
                $number, $createdAt, $email, $person, $status, $payment, $provider,
                ('{0} Market Street' -f (Between 1 220)), $place.city, $place.postal, $place.country,
                $shipping, $discount,
                $item.sku, $item.title, $item.label, $qty, $item.price))
        } else {
            $rows.Add(('{0},,,,,,,,,,,,,{1},{2},{3},{4},{5}' -f
                $number, $item.sku, $item.title, $item.label, $qty, $item.price))
        }
    }
}

$csv = [string]::Join("`n", $rows)
Write-Host ("  importing {0} line(s) for {1} order(s)..." -f ($rows.Count - 1), $Orders)
$result = Invoke-GC POST '/api/admin/import/orders' $csv -Admin

Write-Host ("  created {0}, skipped {1}, {2} error(s), in {3}" -f
    $result.created, $result.skipped, $result.errors.Count, $result.duration) -ForegroundColor DarkGreen
if ($result.errors.Count -gt 0) {
    foreach ($e in ($result.errors | Select-Object -First 5)) {
        Write-Host ("  line {0}: {1}" -f $e.line, $e.message) -ForegroundColor Yellow
    }
}

# ----------------------------------------------------------------- refunds
#
# Issued through the service rather than written into the import, so each one
# leaves an order_refunds row — which is what `gocommerce doctor` reconciles
# refunded_minor against, and what would put the red band on the reports chart.
#
# On the reference binary this does nothing, and that is correct rather than
# broken. A refund goes back through the provider that took the money, and a
# module-free build installs only cash on delivery, which cannot refund. So the
# demo store has no refunds unless you seed it against a build with a real
# payment module. The alternative — importing orders as `payment_status:
# refunded` — sets refunded_minor with no refund record behind it and fails the
# doctor's refund check on every order, which is a worse kind of nothing.

Write-GCStep 'Refunds'
$refunded = 0
$refundedMinor = 0
$refusal = ''
$candidates = @(Invoke-GC GET '/api/admin/orders?limit=200&status=delivered&payment_status=paid' -Admin)
foreach ($o in $candidates) {
    # Roughly one in twelve, and usually part of the order rather than all of
    # it, because a store where every refund is total is not a real store.
    if ((Between 1 12) -ne 1) { continue }
    $amount = [int][math]::Floor($o.total.amount_minor / (Pick @(1, 2, 3, 4)))
    if ($amount -lt 100) { continue }
    try {
        Invoke-GC POST "/api/admin/orders/$($o.id)/refund" @{ amount_minor = $amount; reason = 'Returned by the customer' } -Admin | Out-Null
        $refunded++
        $refundedMinor += $amount
    } catch {
        if (-not $refusal) { $refusal = "$_" }
    }
}
if ($refunded -gt 0) {
    Write-Host ("  {0} refund(s) totalling {1} minor unit(s)" -f $refunded, $refundedMinor) -ForegroundColor DarkGreen
} else {
    Write-Host "  none issued, across $($candidates.Count) candidate order(s)"
    if ($refusal) { Write-Host "  the store said: $refusal" -ForegroundColor DarkGray }
}

# ------------------------------------------------------------------- carts
#
# Baskets somebody opened and has not checked out, so the Carts screen has
# something to show and the dashboard's cart count is a real number.
#
# These are LIVE carts, not abandoned ones, and that is a limit rather than a
# choice. A cart becomes abandoned when it has sat untouched past Config.CartTTL
# — thirty days by default — and the sweep marks it. There is no admin route
# that sets the state, because the state is a fact about elapsed time rather
# than a setting, and CartTTL is not exposed as a flag on the reference binary.
# So seeding an abandoned cart would mean either waiting a month, rebuilding
# with a shorter TTL, or writing the column directly — and the last of those is
# rule 3. The Carts screen opens on the "Abandoned" filter; switch it to live
# and these are what it shows.
#
# Opened through the public API exactly as a shopper's browser would: POST
# /api/carts, then a line at a time. Nothing here is privileged, which is also
# the point — it exercises the same path a storefront uses.

Write-GCStep "Carts ($Carts live baskets)"

# Only variants that can actually be added: AddLine checks stock, and a basket
# of refusals is not a basket.
$sellable = @()
foreach ($p in (Invoke-GC GET '/api/admin/products?limit=200&status=active' -Admin)) {
    foreach ($v in $p.variants) {
        if (-not $v.active) { continue }
        if ($v.track_inventory -and $v.available -lt 3) { continue }
        $sellable += [pscustomobject]@{ id = $v.id; title = $p.title }
    }
}

if ($sellable.Count -eq 0) {
    Write-Host '  nothing in stock to put in a basket' -ForegroundColor Yellow
} else {
    $opened = 0
    $lines = 0
    $refusal = ''
    for ($i = 0; $i -lt $Carts; $i++) {
        # Two in three carry an email — a shopper who got as far as typing one
        # is the one worth chasing, and the screen filters on exactly that.
        $body = @{}
        if ((Between 1 3) -ne 1) {
            $person = Pick $people
            $handle = ($person -replace '[^a-zA-Z]', '').ToLower()
            $body.email = "$handle@example.com"
        }

        try {
            $cart = Invoke-GC POST '/api/carts' $body
        } catch {
            if (-not $refusal) { $refusal = "$_" }
            continue
        }
        $opened++

        foreach ($n in 1..(Between 1 3)) {
            $item = Pick $sellable
            try {
                Invoke-GC POST "/api/carts/$($cart.id)/line-items" @{
                    variant_id = $item.id
                    quantity   = (Pick @(1, 1, 2, 3))
                } | Out-Null
                $lines++
            } catch {
                # A variant that sold out between the listing and now is a
                # refusal the storefront would get too; it is not a seed error.
                if (-not $refusal) { $refusal = "$_" }
            }
        }
    }
    Write-Host ("  {0} basket(s) opened holding {1} line(s)" -f $opened, $lines) -ForegroundColor DarkGreen
    if ($refusal) { Write-Host ("  some lines were refused: {0}" -f $refusal.Trim()) -ForegroundColor DarkGray }
}

# ------------------------------------------------------------------ images
#
# A product list is mostly a column of thumbnails, and without them every row
# shows the same "no image" placeholder — which reads as a broken catalogue
# rather than an empty one.
#
# These are generated, not photographs: a soft ground with one shape on it,
# coloured from the product's own title so a given product always gets the same
# tile. Enough to make the column look deliberate and to exercise the media
# pipeline end to end.
#
# PNG rather than SVG, and that is forced. `safeExtension` in core/media.go
# deliberately excludes .svg — an SVG can carry script, so the store refuses to
# hand one back under its own extension and files it without one, which the
# media route then serves as text/plain with X-Content-Type-Options: nosniff.
# The browser is right to refuse that, so the thumbnails came out blank. The
# safe list is jpg, png, gif, webp and avif; System.Drawing draws a real PNG
# without a dependency.
#
# Uploaded rather than linked, which is also forced: the panel's CSP is
# `img-src 'self' data: blob:`, so an image on someone else's host is recorded
# happily and then blocked, and `AddURL` rejects a data: URI outright. A file
# the store serves itself is the only route that renders. The server needs
# GOCOMMERCE_MEDIA_DIR set; without it the upload answers 501 and this reports
# that rather than failing the seed.

Write-GCStep 'Product images'

Add-Type -AssemblyName System.Drawing

$palette = @(
    @('#EEF1EC', '#C2CBBD'), @('#ECEEF2', '#BFC6D2'), @('#F2EEEA', '#D2C5B8'),
    @('#EAF0F0', '#BCD0D0'), @('#F1EDF2', '#CCC0D2'), @('#F2F0E8', '#D4CDB4')
)

function New-ProductTile {
    param([string]$Title, [string]$Path)

    # Deterministic from the title, so re-running produces the same catalogue.
    $sum = 0
    foreach ($ch in $Title.ToCharArray()) { $sum += [int]$ch }
    $pair = $palette[$sum % $palette.Count]

    $bmp = New-Object System.Drawing.Bitmap 600, 600
    $g = [System.Drawing.Graphics]::FromImage($bmp)
    try {
        $g.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::AntiAlias
        $g.Clear([System.Drawing.ColorTranslator]::FromHtml($pair[0]))
        $brush = New-Object System.Drawing.SolidBrush ([System.Drawing.ColorTranslator]::FromHtml($pair[1]))
        try {
            switch ($sum % 3) {
                0 {
                    $g.FillEllipse($brush, 170, 140, 260, 260)
                    $g.FillRectangle($brush, 170, 440, 260, 50)
                }
                1 {
                    $g.FillRectangle($brush, 170, 170, 260, 290)
                    $g.FillRectangle($brush, 240, 120, 120, 60)
                }
                2 {
                    $points = @(
                        (New-Object System.Drawing.Point(300, 140)),
                        (New-Object System.Drawing.Point(440, 430)),
                        (New-Object System.Drawing.Point(160, 430))
                    )
                    $g.FillPolygon($brush, $points)
                    $g.FillEllipse($brush, 266, 452, 68, 68)
                }
            }
        } finally { $brush.Dispose() }
        $bmp.Save($Path, [System.Drawing.Imaging.ImageFormat]::Png)
    } finally {
        $g.Dispose()
        $bmp.Dispose()
    }
}

$tileDir = Join-Path $env:TEMP 'gc-tiles'
New-Item -ItemType Directory -Force -Path $tileDir | Out-Null

$imaged = 0
$skipped = 0
$refusal = ''
foreach ($p in (Invoke-GC GET '/api/admin/products?limit=200' -Admin)) {
    # One is enough to fill the thumbnail column, and a product that already has
    # media keeps it, so a re-run does not pile up duplicates.
    if ($p.image_url) { $skipped++; continue }

    $file = Join-Path $tileDir ("{0}.png" -f $p.id)
    New-ProductTile -Title $p.title -Path $file

    $raw = & curl.exe -s -X POST `
        -H "Authorization: Bearer $(Get-GCToken)" `
        -F "file=@$file;type=image/png" `
        -F "alt=$($p.title)" `
        "$(Get-GCBase)/api/admin/media"
    try { $media = ($raw | ConvertFrom-Json).data } catch { $media = $null }
    if (-not $media) {
        if (-not $refusal) { $refusal = $raw }
        continue
    }

    try {
        Invoke-GC PUT "/api/admin/products/$($p.id)/media" @{ media_ids = @($media.id) } -Admin | Out-Null
        $imaged++
    } catch {
        if (-not $refusal) { $refusal = "$_" }
    }
    if ($imaged -gt 0 -and $imaged % 20 -eq 0) { Write-Host "  ...$imaged images" }
}

if ($imaged -gt 0) {
    Write-Host ("  {0} product(s) given a tile, {1} already had one" -f $imaged, $skipped) -ForegroundColor DarkGreen
} else {
    Write-Host "  none uploaded, $skipped already had one"
}
if ($refusal) { Write-Host ("  the store said: {0}" -f $refusal.Trim()) -ForegroundColor DarkGray }

# ------------------------------------------------------------------ done

Write-Host ''
Write-Host 'Demo store ready.' -ForegroundColor Green
Write-Host ("  {0} new product(s), {1} sellable skus, {2} order(s) imported over {3} days" -f
    $madeProducts, $skuPool.Count, $result.created, $Days)
Write-Host "  Panel:   $(Get-GCBase)/"
Write-Host "  Reports: $(Get-GCBase)/reports"
