// Transparent-egress verification fixture.
//
// Plain Node fetch, NO proxy config and NO CA config — it must work purely
// because (a) the inner container's egress is transparently captured by the
// firewall proxy, and (b) the image was built via the ~/bin/docker wrapper,
// which baked the mitmproxy CA into this image's trust store.
//
// Success criterion: HTTP 200 from the Scout docs page.
const TARGET = "https://scoutapm.com/docs/node/troubleshooting";

(async () => {
  try {
    const res = await fetch(TARGET);
    const body = await res.text();
    const title = (body.match(/<title>([^<]*)<\/title>/i) || [])[1] || "n/a";
    console.log(`[result] SUCCESS status=${res.status} bytes=${body.length} title="${title}"`);
  } catch (e) {
    console.log(`[result] FAIL ${e.message}`);
  }
})();
