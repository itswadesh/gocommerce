package sitemaps

import (
	"net/http"
	"strings"
)

// A sitemap a person can read.
//
// A sitemap is written for crawlers, and every browser that opens one shows
// either a wall of angle brackets or, in Chrome's case, a bare tree with no
// indication of what any of it is. That matters more than it sounds: the first
// thing anybody does after switching this plugin on is click the address to
// see whether it worked, and raw XML does not answer that question — it does
// not say how many URLs are in there, when they were last changed, or that
// this file is an index pointing at three others.
//
// So each response carries an xml-stylesheet instruction and the stylesheet is
// served beside it. Crawlers ignore the instruction entirely: it is a
// processing instruction, not content, and Google's own documentation says a
// stylesheet has no effect on how a sitemap is read. Browsers apply it and
// render a table.
//
// The XSLT is XSLT 1.0 because that is what every browser implements, and it
// is inlined rather than read from disk for the reason the taxonomy file is
// embedded: a file that has to exist beside the binary is a file that is
// missing in somebody's container.

// stylesheetPath is where the sheet is served and what the instruction points
// at. Relative to the sitemap directory so it works under any host, including
// one behind a proxy that rewrites the scheme.
const stylesheetPath = "/x/sitemaps/sitemap.xsl"

// stylesheetPI is the processing instruction that goes between the XML
// declaration and the root element.
const stylesheetPI = `<?xml-stylesheet type="text/xsl" href="` + stylesheetPath + `"?>` + "\n"

func (m *Module) handleStylesheet(w http.ResponseWriter, r *http.Request) {
	// No plugin check: a stylesheet for a sitemap that is switched off is a
	// stylesheet for nothing, and answering 404 here would leave a browser
	// showing raw XML during the second between enabling the plugin and the
	// first fetch. It contains nothing about the store.
	w.Header().Set("Content-Type", "text/xsl; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write([]byte(sitemapXSL))
}

// sitemapXSL renders both shapes this module serves: the index, whose entries
// are other sitemaps, and a urlset, whose entries are pages. One sheet for
// both, because a reader following the index into products.xml should not find
// a differently-styled page on the other side.
//
// Everything is inline: a stylesheet that pulled a CSS file would be a second
// request that can 404, and the browser applies this one outside the page's
// own origin rules.
var sitemapXSL = strings.ReplaceAll(`<?xml version="1.0" encoding="UTF-8"?>
<xsl:stylesheet version="1.0"
    xmlns:xsl="http://www.w3.org/1999/XSL/Transform"
    xmlns:s="http://www.sitemaps.org/schemas/sitemap/0.9">
  <xsl:output method="html" encoding="UTF-8" indent="yes"/>

  <xsl:template match="/">
    <html>
      <head>
        <title>Sitemap</title>
        <meta name="viewport" content="width=device-width, initial-scale=1"/>
        <style>
          :root {
            color-scheme: light dark;
            --ink: #1f2328; --hint: #656d76; --line: #d8dee4;
            --ground: #ffffff; --panel: #f6f8fa; --link: #0969da;
          }
          @media (prefers-color-scheme: dark) {
            :root {
              --ink: #e6edf3; --hint: #8b949e; --line: #30363d;
              --ground: #0d1117; --panel: #161b22; --link: #4493f8;
            }
          }
          body {
            margin: 0; padding: 32px 16px; background: var(--ground); color: var(--ink);
            font: 14px/1.5 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
          }
          .wrap { max-width: 980px; margin: 0 auto; }
          h1 { margin: 0 0 4px; font-size: 20px; font-weight: 600; }
          .lead { margin: 0 0 20px; color: var(--hint); max-width: 70ch; }
          .count { font-variant-numeric: tabular-nums; }
          table { width: 100%; border-collapse: collapse; border: 1px solid var(--line); border-radius: 8px; overflow: hidden; }
          th, td { padding: 9px 12px; text-align: left; border-bottom: 1px solid var(--line); }
          th { background: var(--panel); font-size: 12px; text-transform: uppercase; letter-spacing: .04em; color: var(--hint); font-weight: 600; white-space: nowrap; }
          tr:last-child td { border-bottom: 0; }
          td.n { width: 1%; white-space: nowrap; color: var(--hint); font-variant-numeric: tabular-nums; }
          td.when { width: 1%; white-space: nowrap; color: var(--hint); font-variant-numeric: tabular-nums; }
          a { color: var(--link); text-decoration: none; word-break: break-all; }
          a:hover { text-decoration: underline; }
          .foot { margin-top: 16px; color: var(--hint); font-size: 12px; }
        </style>
      </head>
      <body>
        <div class="wrap">
          <xsl:apply-templates/>
        </div>
      </body>
    </html>
  </xsl:template>

  <!-- The index: each row is another sitemap. -->
  <xsl:template match="s:sitemapindex">
    <h1>Sitemap index</h1>
    <p class="lead">
      This file does not list pages. It points at
      <span class="count"><xsl:value-of select="count(s:sitemap)"/></span>
      other sitemaps, each of which lists its own. Submit this address to a
      search engine and it will follow them all.
    </p>
    <table>
      <tr><th>#</th><th>Sitemap</th><th>Last changed</th></tr>
      <xsl:for-each select="s:sitemap">
        <tr>
          <td class="n"><xsl:value-of select="position()"/></td>
          <td><a href="{s:loc}"><xsl:value-of select="s:loc"/></a></td>
          <td class="when"><xsl:value-of select="s:lastmod"/></td>
        </tr>
      </xsl:for-each>
    </table>
    <p class="foot">Styled for reading. A crawler ignores this and reads the XML underneath.</p>
  </xsl:template>

  <!-- A urlset: each row is a page on the storefront. -->
  <xsl:template match="s:urlset">
    <h1>Sitemap</h1>
    <p class="lead">
      <span class="count"><xsl:value-of select="count(s:url)"/></span>
      addresses, built from what is on sale right now.
    </p>
    <table>
      <tr><th>#</th><th>Address</th><th>Last changed</th></tr>
      <xsl:for-each select="s:url">
        <tr>
          <td class="n"><xsl:value-of select="position()"/></td>
          <td><a href="{s:loc}"><xsl:value-of select="s:loc"/></a></td>
          <td class="when"><xsl:value-of select="s:lastmod"/></td>
        </tr>
      </xsl:for-each>
    </table>
    <p class="foot">Styled for reading. A crawler ignores this and reads the XML underneath.</p>
  </xsl:template>
</xsl:stylesheet>
`, "\r\n", "\n")
