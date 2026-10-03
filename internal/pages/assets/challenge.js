(function () {
  "use strict";
  // Solves the proof of work for the Xibalba challenge page: find a number n
  // so that SHA-256(nonce + n) starts with the required number of zero bits,
  // then send n back. The page works without this script; then the visitor
  // uses the button instead.
  var form = document.getElementById("xibalba-form");
  var status = document.getElementById("xibalba-status");
  if (!form || !status || !window.Uint32Array || !Math.clz32) { return; }

  var nonce = form.getAttribute("data-nonce") || "";
  var bits = parseInt(form.getAttribute("data-difficulty"), 10);
  if (nonce.length === 0 || nonce.length > 40 || !(bits >= 1 && bits <= 32)) { return; }

  // From here on the script takes over: hide the path without JavaScript
  // and tell the visitor, including screen readers, that the check runs.
  var manual = document.getElementById("xibalba-manual");
  if (manual) { manual.hidden = true; }
  status.hidden = false;
  status.textContent = form.getAttribute("data-working") || "";

  var K = new Uint32Array([0x428a2f98,0x71374491,0xb5c0fbcf,0xe9b5dba5,0x3956c25b,0x59f111f1,0x923f82a4,0xab1c5ed5,0xd807aa98,0x12835b01,0x243185be,0x550c7dc3,0x72be5d74,0x80deb1fe,0x9bdc06a7,0xc19bf174,0xe49b69c1,0xefbe4786,0x0fc19dc6,0x240ca1cc,0x2de92c6f,0x4a7484aa,0x5cb0a9dc,0x76f988da,0x983e5152,0xa831c66d,0xb00327c8,0xbf597fc7,0xc6e00bf3,0xd5a79147,0x06ca6351,0x14292967,0x27b70a85,0x2e1b2138,0x4d2c6dfc,0x53380d13,0x650a7354,0x766a0abb,0x81c2c92e,0x92722c85,0xa2bfe8a1,0xa81a664b,0xc24b8b70,0xc76c51a3,0xd192e819,0xd6990624,0xf40e3585,0x106aa070,0x19a4c116,0x1e376c08,0x2748774c,0x34b0bcb5,0x391c0cb3,0x4ed8aa4a,0x5b9cca4f,0x682e6ff3,0x748f82ee,0x78a5636f,0x84c87814,0x8cc70208,0x90befffa,0xa4506ceb,0xbef9a3f7,0xc67178f2]);
  var W = new Uint32Array(64);
  var block = new Uint8Array(64);
  var base = nonce.length;
  var i;
  for (i = 0; i < base; i++) { block[i] = nonce.charCodeAt(i) & 255; }

  // firstWord returns the first 32 bits of SHA-256(nonce + digits of n).
  // The message always fits one 64-byte block (at most 55 bytes).
  function firstWord(n) {
    var s = String(n), len = base + s.length, j, x, y, t1, t2;
    for (j = 0; j < s.length; j++) { block[base + j] = s.charCodeAt(j); }
    block[len] = 128;
    for (j = len + 1; j < 62; j++) { block[j] = 0; }
    block[62] = (len * 8) >>> 8;
    block[63] = (len * 8) & 255;
    for (j = 0; j < 16; j++) {
      W[j] = (block[4 * j] << 24) | (block[4 * j + 1] << 16) | (block[4 * j + 2] << 8) | block[4 * j + 3];
    }
    for (j = 16; j < 64; j++) {
      x = W[j - 15]; y = W[j - 2];
      W[j] = (W[j - 16] + (((x >>> 7) | (x << 25)) ^ ((x >>> 18) | (x << 14)) ^ (x >>> 3)) +
              W[j - 7] + (((y >>> 17) | (y << 15)) ^ ((y >>> 19) | (y << 13)) ^ (y >>> 10))) | 0;
    }
    var a = 0x6a09e667 | 0, b = 0xbb67ae85 | 0, c = 0x3c6ef372 | 0, d = 0xa54ff53a | 0;
    var e = 0x510e527f | 0, f = 0x9b05688c | 0, g = 0x1f83d9ab | 0, h = 0x5be0cd19 | 0;
    for (j = 0; j < 64; j++) {
      t1 = (h + (((e >>> 6) | (e << 26)) ^ ((e >>> 11) | (e << 21)) ^ ((e >>> 25) | (e << 7))) +
            ((e & f) ^ (~e & g)) + K[j] + W[j]) | 0;
      t2 = ((((a >>> 2) | (a << 30)) ^ ((a >>> 13) | (a << 19)) ^ ((a >>> 22) | (a << 10))) +
            ((a & b) ^ (a & c) ^ (b & c))) | 0;
      h = g; g = f; f = e; e = (d + t1) | 0; d = c; c = b; b = a; a = (t1 + t2) | 0;
    }
    return (a + 0x6a09e667) >>> 0;
  }

  var n = 0;
  function finish() {
    form.elements.namedItem("method").value = "pow";
    form.elements.namedItem("solution").value = String(n);
    status.textContent = form.getAttribute("data-done") || "";
    form.submit();
  }
  // Work in short slices so the page stays responsive on slow devices.
  function run() {
    var stop = Date.now() + 25, k;
    do {
      for (k = 0; k < 1000; k++) {
        if (Math.clz32(firstWord(n)) >= bits) { finish(); return; }
        n++;
      }
    } while (Date.now() < stop);
    window.setTimeout(run, 0);
  }
  window.setTimeout(run, 30);
})();
