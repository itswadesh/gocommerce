# Fills a store with a sample fashion catalogue: dresses, ethnic wear and
# menswear, photographed on models, in several colours and sizes each.
#
#   .\scripts\seed-fashion.ps1 -BaseUrl http://127.0.0.1:8090 -Token dev-token
#
# The catalogue is scripts\fashion-images\catalogue.json, so what a store gets is
# reviewable as data rather than read out of code. It is priced in Indian rupees
# and written for an Indian store: twelve products, two to four colours each,
# every colour in every size, one variant per colour and size.
#
# Photographs are not in the repository. scripts\fashion-images\generate.mjs
# makes them with Google Gemini into scripts\fashion-images\out\ (git-ignored),
# and this uploads them from there. Without them the products are still
# created, and a later run attaches the pictures once they exist.
#
# Pictures belong to the product and each variant nominates its colour's four
# (PUT /api/admin/variants/{id}/media), which is how a storefront swaps the
# gallery when a shopper picks a colour, and why every size of one colour shows
# the same photographs without any file being uploaded twice.
#
# Everything goes through the API, no SQL. Safe to re-run: products are found by
# their tag and slug, categories and collections by slug, and a product that
# already has pictures keeps them.

param(
    [string]$BaseUrl  = $env:GC_BASE,
    [string]$Token    = $env:GC_TOKEN,
    [string]$ImageDir = (Join-Path $PSScriptRoot 'fashion-images\out'),
    # The catalogue is priced in rupees. A store selling in anything else is
    # refused unless this says to switch it, which the engine allows only
    # before the store's first order.
    [switch]$SetCurrency
)

$ErrorActionPreference = 'Stop'
if ($BaseUrl) { $env:GC_BASE = $BaseUrl }
if ($Token)   { $env:GC_TOKEN = $Token }
. "$PSScriptRoot\gc.ps1"

$catalogue = Get-Content (Join-Path $PSScriptRoot 'fashion-images\catalogue.json') -Raw -Encoding UTF8 | ConvertFrom-Json
Write-Host "Seeding the fashion catalogue into $(Get-GCBase)" -ForegroundColor Cyan

function ConvertTo-Slug([string]$Text) { ($Text.ToLower() -replace '[^a-z0-9]+', '-').Trim('-') }

# ---------------------------------------------------------------- currency

Write-GCStep 'Currency'
$store = Invoke-GC GET '/api/admin/store' -Admin
if ($store.currency -ne $catalogue.currency) {
    if (-not $SetCurrency) {
        throw "this store sells in $($store.currency) and the catalogue is priced in $($catalogue.currency). Run again with -SetCurrency to switch it."
    }
    Invoke-GC PATCH '/api/admin/store' @{ currency = $catalogue.currency } -Admin | Out-Null
    Write-Host "  switched $($store.currency) -> $($catalogue.currency)" -ForegroundColor DarkGreen
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
        Write-Host "  skip  $($c.slug)"
        continue
    } catch { }
    $body = @{ slug = $c.slug; title = $c.title }
    if ($c.parent) { $body.parent_id = $categoryIds[$c.parent] }
    $categoryIds[$c.slug] = (Invoke-GC POST '/api/admin/categories' $body -Admin).id
    Write-Host "  new   $($c.slug)" -ForegroundColor DarkGreen
}

# ------------------------------------------------------------- collections

Write-GCStep 'Collections'
$collectionIds = @{}
foreach ($c in (Invoke-GC GET '/api/admin/collections?limit=200' -Admin)) { $collectionIds[$c.slug] = $c.id }
foreach ($c in $catalogue.collections) {
    if ($collectionIds.ContainsKey($c.slug)) { Write-Host "  skip  $($c.slug)"; continue }
    $made = Invoke-GC POST '/api/admin/collections' @{ slug = $c.slug; title = $c.title; description = $c.description } -Admin
    $collectionIds[$c.slug] = $made.id
    Write-Host "  new   $($c.slug)" -ForegroundColor DarkGreen
}

# ---------------------------------------------------------------- products

Write-GCStep 'Products'

# Found by tag rather than by reading the whole catalogue, which a store with
# thousands of products would page through for twelve slugs.
$existing = @{}
foreach ($p in (Invoke-GC GET "/api/admin/products?tag=$($catalogue.tag)&limit=200" -Admin)) { $existing[$p.slug] = $p }

$created = 0
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
        slug = $p.slug; title = $p.title; description = $p.description; status = 'active'
        product_type = 'Apparel'; tags = @($catalogue.tag)
        category_id = $categoryIds[$p.category]
        metadata = @{ highlights = @($p.highlights) }
        # Colour first: it is the choice that changes the pictures, so it is
        # the one a product page should ask about first.
        options = @(
            @{ name = 'Colour'; values = @($p.colours | ForEach-Object { $_.name }) },
            @{ name = 'Size'; values = @($p.sizes) }
        )
        variants = $variants
    }
    try {
        $made = Invoke-GC POST '/api/admin/products' $body -Admin
    } catch {
        # A product of the store's own already has this slug: leave it alone.
        if ("$_" -match 'conflict') { Write-Host "  slug or SKU taken, skipped  $($p.slug)" -ForegroundColor Yellow; continue }
        throw
    }
    $existing[$p.slug] = $made
    $created++
    Write-Host ("  new   {0}  ({1} variants)" -f $p.slug, $variants.Count) -ForegroundColor DarkGreen

    $ids = @($p.collections | ForEach-Object { $collectionIds[$_] })
    if ($ids.Count -gt 0) {
        Invoke-GC PUT "/api/admin/products/$($made.id)/collections" @{ collection_ids = $ids } -Admin | Out-Null
    }
}

# ------------------------------------------------------------------ images

Write-GCStep "Photographs (from $ImageDir)"

function Send-Media([string]$File, [string]$Alt) {
    $curlArgs = @('-s', '-X', 'POST', '-H', "Authorization: Bearer $(Get-GCToken)")
    if ($env:GC_STORE) { $curlArgs += @('-H', "x-litekart-store: $env:GC_STORE") }
    $curlArgs += @('-F', "file=@$File;type=image/webp", '-F', "alt=$Alt", "$(Get-GCBase)/api/admin/media")
    $raw = & curl.exe @curlArgs
    try { $media = ($raw | ConvertFrom-Json).data } catch { $media = $null }
    if (-not $media) { throw "upload of $File refused: $raw" }
    $media.id
}

$pictured = 0
$waiting = @()
foreach ($p in $catalogue.products) {
    $have = $existing[$p.slug]
    if (-not $have) { continue }
    if ($have.media_count -gt 0) { Write-Host "  skip  $($p.slug) (has pictures)"; continue }

    # All of a product's files are checked before any is uploaded, so a half-
    # generated set never leaves a product showing some colours and not others.
    $files = @{}
    $missing = 0
    foreach ($colour in $p.colours) {
        $files[$colour.name] = @(foreach ($shot in $catalogue.shots) {
            $f = Join-Path $ImageDir ("{0}\{1}-{2}.webp" -f $p.slug, (ConvertTo-Slug $colour.name), $shot.name)
            if (-not (Test-Path $f)) { $missing++ }
            [pscustomobject]@{ file = $f; shot = $shot.name }
        })
    }
    if ($missing -gt 0) { $waiting += $p.slug; continue }

    $byColour = @{}
    $all = @()
    foreach ($colour in $p.colours) {
        $byColour[$colour.name] = @(foreach ($f in $files[$colour.name]) {
            $view = if ($f.shot -eq 'detail') { 'close-up of the fabric' } else { "$($f.shot) view" }
            Send-Media $f.file "$($p.title) in $($colour.name.ToLower()), $view"
        })
        $all += $byColour[$colour.name]
    }
    Invoke-GC PUT "/api/admin/products/$($have.id)/media" @{ media_ids = $all } -Admin | Out-Null

    $full = Invoke-GC GET "/api/admin/products/$($have.id)" -Admin
    foreach ($v in $full.variants) {
        $colour = $p.colours | Where-Object { $v.options -contains $_.name } | Select-Object -First 1
        if (-not $colour) { continue }
        Invoke-GC PUT "/api/admin/variants/$($v.id)/media" @{ media_ids = $byColour[$colour.name] } -Admin | Out-Null
    }
    $pictured++
    Write-Host ("  new   {0}  ({1} pictures)" -f $p.slug, $all.Count) -ForegroundColor DarkGreen
}
if ($waiting.Count -gt 0) {
    Write-Host ("  {0} product(s) wait for photographs: {1}" -f $waiting.Count, ($waiting -join ', ')) -ForegroundColor Yellow
    Write-Host '  generate them with scripts\fashion-images\generate.mjs, then run this again' -ForegroundColor Yellow
}

Write-Host ''
Write-Host ("Done: {0} product(s) created, {1} given photographs." -f $created, $pictured) -ForegroundColor Green
