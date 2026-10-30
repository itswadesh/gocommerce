# Fills a store with a sample fashion catalogue: dresses, ethnic wear and
# menswear, photographed on models, in several colours and sizes each.
#
#   .\scripts\seed-fashion.ps1 -BaseUrl http://127.0.0.1:8090 -Token dev-token
#   .\scripts\seed-fashion.ps1 -BaseUrl https://store.example -Token <token> -Draft
#
# The catalogue is core\sample\catalogue.json, the one a new store's sample shop
# is made from (D159), so what a store gets is reviewable as data rather than
# read out of code. It is priced in Indian rupees and written for an Indian
# store: eleven products, three or four colours each, every colour in every
# size, one variant per colour and size.
#
# The full-size photographs are not in the repository; the engine carries
# smaller copies for the sample shop. They are free stock photographs
# (scripts\fashion-images\CREDITS.md) of each product in its first colour, which
# scripts\fashion-images\fetch-photos.mjs downloads into the git-ignored
# scripts\fashion-images\out\. The other colours are edits of those, saved
# beside them under their own colour's name (recolour.json lists them). This
# uploads whatever is there: a colour whose files are not yet there gets no
# pictures, and a later run attaches them once they exist.
#
# Pictures belong to the product and each variant nominates its colour's
# (PUT /api/admin/variants/{id}/media), which is how a storefront swaps the
# gallery when a shopper picks a colour, and why every size of one colour shows
# the same photographs without any file being uploaded twice.
#
# Without -Draft it is for a dev or demo store: the prices, the struck-through
# MRPs and the "only a few left" stock are invented to exercise the storefront,
# not facts about any shop. -Draft is for a live store, where the owner sets
# real prices and publishes each product: shoppers see nothing new.
#
#   - Products are created as drafts. One that already exists is left alone,
#     whatever its status, so a run never re-publishes or un-publishes it.
#   - No category or collection is created. The engine has no hidden flag for
#     either, and the public category listing returns every category whether
#     anything is filed in it or not, so an empty one would show up in a
#     storefront's navigation. A product is filed under its category or
#     collections only if the store already has them (a draft in a category or
#     collection is not shown in it), and a later run files a draft under
#     ones created since. The rest is printed at the end as a filing list for
#     the owner, and catalogue.json keeps it. It is not
#     written on the product: storefronts print a product's metadata on its
#     page and its tags as filters, so a note there would be shown to
#     shoppers the moment the owner published it.
#   - Every product carries the catalogue's tag, which is how a re-run finds
#     them, and a storefront shows it once the product is published. The owner
#     can remove it then: a later run finds the slug taken and skips it.
#
# Everything goes through the API, no SQL. Safe to re-run: products are found by
# their tag and slug, categories and collections by slug, and a colour whose
# variants already have pictures keeps them. An upload that fails part way
# leaves the files it did send unattached in the media library; the next run
# uploads that colour again.

param(
    [string]$BaseUrl  = $env:GC_BASE,
    [string]$Token    = $env:GC_TOKEN,
    [string]$ImageDir = (Join-Path $PSScriptRoot 'fashion-images\out'),
    # The catalogue is priced in rupees. A store selling in anything else is
    # refused unless this says to switch it, which the engine allows only
    # before the store's first order.
    [switch]$SetCurrency,
    # For a live store: drafts only, and nothing created that a shopper sees.
    [switch]$Draft
)

$ErrorActionPreference = 'Stop'
if ($BaseUrl) { $env:GC_BASE = $BaseUrl }
if ($Token)   { $env:GC_TOKEN = $Token }
. "$PSScriptRoot\gc.ps1"

$catalogue = Get-Content (Join-Path $PSScriptRoot '..\core\sample\catalogue.json') -Raw -Encoding UTF8 | ConvertFrom-Json
$mode = if ($Draft) { ' as hidden drafts' } else { '' }
Write-Host "Seeding the fashion catalogue into $(Get-GCBase)$mode" -ForegroundColor Cyan

function ConvertTo-Slug([string]$Text) { ($Text.ToLower() -replace '[^a-z0-9]+', '-').Trim('-') }

# ---------------------------------------------------------------- currency

Write-GCStep 'Currency'
$store = Invoke-GC GET '/api/admin/store' -Admin
if ($store.currency -ne $catalogue.currency) {
    if (-not $SetCurrency) {
        throw "this store sells in $($store.currency) and the catalogue is priced in $($catalogue.currency). Run again with -SetCurrency to switch it."
    }
    # The whole profile goes back, not just the currency: the save writes every
    # address and contact field, so a body naming only the currency would
    # blank the shop's name, address and tax number.
    $was = $store.currency
    $store.currency = $catalogue.currency
    Invoke-GC PATCH '/api/admin/store' $store -Admin | Out-Null
    Write-Host "  switched $was -> $($catalogue.currency)" -ForegroundColor DarkGreen
} else {
    Write-Host "  $($store.currency)"
}

# -------------------------------------------------------------- categories
#
# Looked up one slug at a time, as seed-demo.ps1 does and for its reason: the
# listing ignores limit and offset, so on a store with the standard taxonomy a
# slug past the first page looks missing.

Write-GCStep 'Categories'
$categoryIds = @{}
foreach ($c in $catalogue.categories) {
    try {
        $categoryIds[$c.slug] = (Invoke-GC GET "/api/categories/$($c.slug)").id
        Write-Host "  have  $($c.slug)"
        continue
    } catch { }
    if ($Draft) { Write-Host "  not created  $($c.slug)"; continue }
    $body = @{ slug = $c.slug; title = $c.title }
    if ($c.parent) { $body.parent_id = $categoryIds[$c.parent] }
    $categoryIds[$c.slug] = (Invoke-GC POST '/api/admin/categories' $body -Admin).id
    Write-Host "  new   $($c.slug)" -ForegroundColor DarkGreen
}

# ------------------------------------------------------------- collections

Write-GCStep 'Collections'
$collectionIds = @{}
# Paged, so a store with more than 200 collections does not hide one of these
# on page two and then refuse to create it as a duplicate.
for ($offset = 0; ; $offset += 200) {
    $page = @(Invoke-GC GET "/api/admin/collections?limit=200&offset=$offset" -Admin)
    foreach ($c in $page) { $collectionIds[$c.slug] = $c.id }
    if ($page.Count -lt 200) { break }
}
foreach ($c in $catalogue.collections) {
    if ($collectionIds.ContainsKey($c.slug)) { Write-Host "  have  $($c.slug)"; continue }
    if ($Draft) { Write-Host "  not created  $($c.slug)"; continue }
    $made = Invoke-GC POST '/api/admin/collections' @{ slug = $c.slug; title = $c.title; description = $c.description } -Admin
    $collectionIds[$c.slug] = $made.id
    Write-Host "  new   $($c.slug)" -ForegroundColor DarkGreen
}

# ---------------------------------------------------------------- products

Write-GCStep 'Products'

# Found by tag rather than by reading the whole catalogue, which a store with
# thousands of products would page through for eleven slugs.
$existing = @{}
foreach ($p in (Invoke-GC GET "/api/admin/products?tag=$($catalogue.tag)&limit=200" -Admin)) { $existing[$p.slug] = $p }

# A typo in the data file is caught before anything is written, rather than
# sent to the engine as a null collection id or filed under nothing.
$knownCategories = @($catalogue.categories | ForEach-Object { $_.slug })
$knownCollections = @($catalogue.collections | ForEach-Object { $_.slug })
foreach ($p in $catalogue.products) {
    foreach ($slug in @($p.collections)) {
        if ($knownCollections -notcontains $slug) { throw "$($p.slug) names an unknown collection '$slug'" }
    }
    if ($knownCategories -notcontains $p.category) { throw "$($p.slug) names an unknown category '$($p.category)'" }
}

$created = 0
$variantsCreated = 0
$index = 0
foreach ($p in $catalogue.products) {
    $index++
    if ($existing.ContainsKey($p.slug)) { Write-Host "  skip  $($p.slug)"; continue }

    # Seeded per product rather than once for the run, so a product's stock is
    # the same whichever of the others already existed.
    $rand = [System.Random]::new(20261028 + $index)
    $variants = @()
    foreach ($colour in $p.colours) {
        foreach ($size in $p.sizes) {
            # A spread the panel and storefront can show: most sizes comfortable,
            # the ones the catalogue names sold out or down to the last few.
            $stock = $rand.Next(6, 31)
            if ($p.sold_out -contains "$($colour.name)/$size") { $stock = 0 }
            elseif ($p.low -contains "$($colour.name)/$size") { $stock = $rand.Next(1, 4) }
            $sizeCode = if ($size -eq 'Free size') { 'FS' } else { $size }
            $v = @{
                sku = "$($p.sku)-$($colour.code)-$sizeCode"
                price_minor = $p.price_minor
                options = @($colour.name, $size)
                stock_on_hand = $stock
                weight_grams = $p.weight_grams
                hs_code = $p.hs_code
            }
            if ($p.compare_at_minor) { $v.compare_at_price_minor = $p.compare_at_minor }
            $variants += $v
        }
    }

    $body = @{
        slug = $p.slug; title = $p.title; description = $p.description
        status = if ($Draft) { 'draft' } else { 'active' }
        product_type = 'Apparel'; tags = @($catalogue.tag)
        metadata = @{ highlights = @($p.highlights) }
        # Colour first: it is the choice that changes the pictures, so it is
        # the one a product page should ask about first.
        options = @(
            @{ name = 'Colour'; values = @($p.colours | ForEach-Object { $_.name }) },
            @{ name = 'Size'; values = @($p.sizes) }
        )
        variants = $variants
    }
    if ($p.badge) { $body.metadata.badge = $p.badge }
    if ($categoryIds.ContainsKey($p.category)) { $body.category_id = $categoryIds[$p.category] }
    try {
        $made = Invoke-GC POST '/api/admin/products' $body -Admin
    } catch {
        # A product of the store's own already has this slug: leave it alone.
        if ("$_" -match 'conflict') { Write-Host "  slug or SKU taken, skipped  $($p.slug)" -ForegroundColor Yellow; continue }
        throw
    }
    $existing[$p.slug] = $made
    $created++
    $variantsCreated += $variants.Count
    Write-Host ("  new   {0}  ({1} variants, {2})" -f $p.slug, $variants.Count, $body.status) -ForegroundColor DarkGreen
}

# Filing, apart from creation: a product whose create succeeded and whose
# collections did not gets them on the next run, and under -Draft a draft made
# before its category or collections existed is filed once they do. Only what
# is still empty is filled, so the owner's own filing stands. Under -Draft only
# drafts are touched: filing a published product would put it on a page.
$unfiled = @()
foreach ($p in $catalogue.products) {
    $have = $existing[$p.slug]
    if (-not $have -or ($Draft -and $have.status -ne 'draft')) { continue }
    if (-not $have.category -and $categoryIds.ContainsKey($p.category)) {
        Invoke-GC PATCH "/api/admin/products/$($have.id)" @{ category_id = $categoryIds[$p.category] } -Admin | Out-Null
        $have | Add-Member -Force NoteProperty category @{ slug = $p.category }
    }
    $ids = @($p.collections | Where-Object { $collectionIds.ContainsKey($_) } | ForEach-Object { $collectionIds[$_] })
    if ($ids.Count -gt 0 -and @($have.collections).Count -eq 0) {
        Invoke-GC PUT "/api/admin/products/$($have.id)/collections" @{ collection_ids = $ids } -Admin | Out-Null
        $have | Add-Member -Force NoteProperty collections @($ids)
    }
    if (-not $have.category -or (@($p.collections).Count -gt 0 -and @($have.collections).Count -eq 0)) { $unfiled += $p }
}

# ------------------------------------------------------------------ images
#
# Colour by colour: a colour whose variants all have pictures is done, and one
# whose variants have none gets its files uploaded, if they are all there. A
# colour's uploads are found again by their alt text, so a run that stopped
# between attaching them to the product and nominating them for the variants
# finishes the job instead of uploading the set twice.

Write-GCStep "Photographs (from $ImageDir)"

function Send-Media([string]$File, [string]$Alt) {
    $curlArgs = @('-s', '-X', 'POST', '-H', "Authorization: Bearer $(Get-GCToken)")
    if ($env:GC_STORE) { $curlArgs += @('-H', "x-litekart-store: $env:GC_STORE") }
    # --form-string for the alt text, so a title with a semicolon is not read
    # as a curl ;type= directive.
    $curlArgs += @('-F', "file=@$File;type=image/webp", '--form-string', "alt=$Alt", "$(Get-GCBase)/api/admin/media")
    $raw = & curl.exe @curlArgs
    try { $media = ($raw | ConvertFrom-Json).data } catch { $media = $null }
    if (-not $media) { throw "upload of $File refused: $raw" }
    $media.id
}

function Get-Alt($Product, $Colour, [string]$Shot) {
    $view = if ($Shot -eq 'detail') { 'close-up' } else { "$Shot view" }
    "$($Product.title) in $($Colour.name.ToLower()), $view"
}

$uploaded = 0
$waiting = @()
foreach ($p in $catalogue.products) {
    $have = $existing[$p.slug]
    if (-not $have) { continue }
    $full = Invoke-GC GET "/api/admin/products/$($have.id)" -Admin
    # Once the owner has published a product its pictures are theirs to
    # change: a -Draft run adding a colour's photographs would change a page
    # shoppers can see.
    if ($Draft -and $full.status -ne 'draft') { Write-Host "  skip  $($p.slug) (not a draft)"; continue }
    $attached = @(Invoke-GC GET "/api/admin/products/$($have.id)/media" -Admin)
    $missingColours = @()
    foreach ($colour in $p.colours) {
        $mine = @($full.variants | Where-Object { $_.options -contains $colour.name })
        # `-not`, not a count: a variant with none has no images key at all, and
        # @($null).Count is 1 in Windows PowerShell.
        $bare = @($mine | Where-Object { -not $_.images })
        if ($bare.Count -eq 0) { continue }

        # One id per shot: an upload already attached under that shot's alt
        # text, or $null for a shot still to upload.
        $alts = @($p.photos | ForEach-Object { Get-Alt $p $colour $_.shot })
        $ids = @(foreach ($alt in $alts) { ($attached | Where-Object { $_.alt -eq $alt } | Select-Object -First 1).id })
        $todo = @(for ($i = 0; $i -lt $alts.Count; $i++) { if (-not $ids[$i]) { $i } })
        if ($todo.Count -gt 0) {
            # All of a colour's files are checked before any is uploaded, so a
            # colour never shows some of its shots and not the others.
            $files = @($p.photos | ForEach-Object { Join-Path $ImageDir ("{0}\{1}-{2}.webp" -f $p.slug, (ConvertTo-Slug $colour.name), $_.shot) })
            if (@($todo | Where-Object { -not (Test-Path $files[$_]) }).Count -gt 0) { $missingColours += $colour.name; continue }
            foreach ($i in $todo) { $ids[$i] = Send-Media $files[$i] $alts[$i] }
            $uploaded += $todo.Count
            $all = @($attached | ForEach-Object { $_.id }) + @($todo | ForEach-Object { $ids[$_] })
            Invoke-GC PUT "/api/admin/products/$($have.id)/media" @{ media_ids = $all } -Admin | Out-Null
            $attached = @(Invoke-GC GET "/api/admin/products/$($have.id)/media" -Admin)
        }
        foreach ($v in $bare) {
            Invoke-GC PUT "/api/admin/variants/$($v.id)/media" @{ media_ids = $ids } -Admin | Out-Null
        }
        Write-Host ("  {0}  {1} ({2} pictures)" -f $p.slug, $colour.name, $ids.Count) -ForegroundColor DarkGreen
    }
    if ($missingColours.Count -gt 0) { $waiting += "$($p.slug) ($($missingColours -join ', '))" }
}
if ($waiting.Count -gt 0) {
    Write-Host ("  {0} product(s) have colours waiting for photographs:" -f $waiting.Count) -ForegroundColor Yellow
    $waiting | ForEach-Object { Write-Host "    $_" -ForegroundColor Yellow }
    Write-Host '  see scripts\fashion-images\fetch-photos.mjs and recolour.json, then run this again' -ForegroundColor Yellow
}

# The filing still to do, by the owner in the panel or by a later run once the
# categories and collections exist; see the header for why it is not written
# on the products themselves.
if ($unfiled.Count -gt 0) {
    Write-GCStep 'Not filed yet (create these categories and collections, or file them in the panel)'
    foreach ($p in $unfiled) {
        $cat = $catalogue.categories | Where-Object { $_.slug -eq $p.category }
        $parent = $catalogue.categories | Where-Object { $_.slug -eq $cat.parent }
        $path = if ($parent) { "$($parent.title) / $($cat.title)" } else { $cat.title }
        $cols = if (@($p.collections).Count) { "; collections: $(@($p.collections) -join ', ')" } else { '' }
        Write-Host "    $($p.slug)  ->  $path$cols"
    }
}

Write-Host ''
$as = if ($Draft) { ' as drafts' } else { '' }
Write-Host ("Done: {0} product(s) with {1} variants created{2}, {3} photograph(s) uploaded." -f $created, $variantsCreated, $as, $uploaded) -ForegroundColor Green
