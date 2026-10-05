(function () {
  "use strict";
  // The memory-hard function of the Xibalba challenge page (method
  // "pow-memory"): scrypt (RFC 7914) with r = 8 and p = 1. It is only on
  // the page when that method is asked for. The script that follows does
  // the searching and calls start(text, N) for each candidate; step(until)
  // then works until the time "until" and returns the 32-byte value when
  // it is done, null before. Working in steps keeps the page responsive.
  if (!window.Uint32Array || !window.Uint8Array) { return; }

  var K = new Uint32Array([0x428a2f98,0x71374491,0xb5c0fbcf,0xe9b5dba5,0x3956c25b,0x59f111f1,0x923f82a4,0xab1c5ed5,0xd807aa98,0x12835b01,0x243185be,0x550c7dc3,0x72be5d74,0x80deb1fe,0x9bdc06a7,0xc19bf174,0xe49b69c1,0xefbe4786,0x0fc19dc6,0x240ca1cc,0x2de92c6f,0x4a7484aa,0x5cb0a9dc,0x76f988da,0x983e5152,0xa831c66d,0xb00327c8,0xbf597fc7,0xc6e00bf3,0xd5a79147,0x06ca6351,0x14292967,0x27b70a85,0x2e1b2138,0x4d2c6dfc,0x53380d13,0x650a7354,0x766a0abb,0x81c2c92e,0x92722c85,0xa2bfe8a1,0xa81a664b,0xc24b8b70,0xc76c51a3,0xd192e819,0xd6990624,0xf40e3585,0x106aa070,0x19a4c116,0x1e376c08,0x2748774c,0x34b0bcb5,0x391c0cb3,0x4ed8aa4a,0x5b9cca4f,0x682e6ff3,0x748f82ee,0x78a5636f,0x84c87814,0x8cc70208,0x90befffa,0xa4506ceb,0xbef9a3f7,0xc67178f2]);
  var W = new Uint32Array(64);

  // sha256 of the given pieces (Uint8Arrays) one after the other.
  function sha256(pieces) {
    var total = 0, i, j, p, at = 0;
    for (i = 0; i < pieces.length; i++) { total += pieces[i].length; }
    var padded = new Uint8Array(((total + 9 + 63) >> 6) << 6);
    for (i = 0; i < pieces.length; i++) { padded.set(pieces[i], at); at += pieces[i].length; }
    padded[total] = 128;
    var bitsLen = total * 8;
    padded[padded.length - 4] = bitsLen >>> 24;
    padded[padded.length - 3] = (bitsLen >>> 16) & 255;
    padded[padded.length - 2] = (bitsLen >>> 8) & 255;
    padded[padded.length - 1] = bitsLen & 255;
    var H = new Uint32Array([0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19]);
    var a, b, c, d, e, f, g, h, x, y, t1, t2;
    for (p = 0; p < padded.length; p += 64) {
      for (j = 0; j < 16; j++) {
        W[j] = (padded[p + 4 * j] << 24) | (padded[p + 4 * j + 1] << 16) | (padded[p + 4 * j + 2] << 8) | padded[p + 4 * j + 3];
      }
      for (j = 16; j < 64; j++) {
        x = W[j - 15]; y = W[j - 2];
        W[j] = (W[j - 16] + (((x >>> 7) | (x << 25)) ^ ((x >>> 18) | (x << 14)) ^ (x >>> 3)) +
                W[j - 7] + (((y >>> 17) | (y << 15)) ^ ((y >>> 19) | (y << 13)) ^ (y >>> 10))) | 0;
      }
      a = H[0] | 0; b = H[1] | 0; c = H[2] | 0; d = H[3] | 0; e = H[4] | 0; f = H[5] | 0; g = H[6] | 0; h = H[7] | 0;
      for (j = 0; j < 64; j++) {
        t1 = (h + (((e >>> 6) | (e << 26)) ^ ((e >>> 11) | (e << 21)) ^ ((e >>> 25) | (e << 7))) +
              ((e & f) ^ (~e & g)) + K[j] + W[j]) | 0;
        t2 = ((((a >>> 2) | (a << 30)) ^ ((a >>> 13) | (a << 19)) ^ ((a >>> 22) | (a << 10))) +
              ((a & b) ^ (a & c) ^ (b & c))) | 0;
        h = g; g = f; f = e; e = (d + t1) | 0; d = c; c = b; b = a; a = (t1 + t2) | 0;
      }
      H[0] += a; H[1] += b; H[2] += c; H[3] += d; H[4] += e; H[5] += f; H[6] += g; H[7] += h;
    }
    var out = new Uint8Array(32);
    for (i = 0; i < 8; i++) {
      out[4 * i] = H[i] >>> 24; out[4 * i + 1] = (H[i] >>> 16) & 255; out[4 * i + 2] = (H[i] >>> 8) & 255; out[4 * i + 3] = H[i] & 255;
    }
    return out;
  }

  // PBKDF2-HMAC-SHA256 with one round: the first "length" bytes.
  function pbkdf2(password, salt, length) {
    var key = password.length > 64 ? sha256([password]) : password;
    var inner = new Uint8Array(64), outer = new Uint8Array(64), i;
    for (i = 0; i < 64; i++) {
      inner[i] = (key[i] || 0) ^ 0x36;
      outer[i] = (key[i] || 0) ^ 0x5c;
    }
    var out = new Uint8Array(length), count = new Uint8Array(4), block, n = 1;
    for (i = 0; i < length; i += 32, n++) {
      count[0] = n >>> 24; count[1] = (n >>> 16) & 255; count[2] = (n >>> 8) & 255; count[3] = n & 255;
      block = sha256([outer, sha256([inner, salt, count])]);
      out.set(length - i < 32 ? block.subarray(0, length - i) : block, i);
    }
    return out;
  }

  // One block is 128 * r bytes = 256 words for r = 8.
  var WORDS = 256;
  var X = new Uint32Array(WORDS), T = new Uint32Array(WORDS), S = new Uint32Array(16);
  var V = null;

  // blockMix is scrypt's BlockMix with the Salsa20/8 core, in place on X.
  function blockMix() {
    var i, k, o, to;
    var x0, x1, x2, x3, x4, x5, x6, x7, x8, x9, x10, x11, x12, x13, x14, x15, u, round;
    for (k = 0; k < 16; k++) { S[k] = X[WORDS - 16 + k]; }
    for (i = 0; i < 16; i++) {
      o = i * 16;
      x0 = (S[0] ^= X[o]); x1 = (S[1] ^= X[o + 1]); x2 = (S[2] ^= X[o + 2]); x3 = (S[3] ^= X[o + 3]);
      x4 = (S[4] ^= X[o + 4]); x5 = (S[5] ^= X[o + 5]); x6 = (S[6] ^= X[o + 6]); x7 = (S[7] ^= X[o + 7]);
      x8 = (S[8] ^= X[o + 8]); x9 = (S[9] ^= X[o + 9]); x10 = (S[10] ^= X[o + 10]); x11 = (S[11] ^= X[o + 11]);
      x12 = (S[12] ^= X[o + 12]); x13 = (S[13] ^= X[o + 13]); x14 = (S[14] ^= X[o + 14]); x15 = (S[15] ^= X[o + 15]);
      for (round = 0; round < 4; round++) {
        u = (x0 + x12) | 0; x4 ^= (u << 7) | (u >>> 25);
        u = (x4 + x0) | 0; x8 ^= (u << 9) | (u >>> 23);
        u = (x8 + x4) | 0; x12 ^= (u << 13) | (u >>> 19);
        u = (x12 + x8) | 0; x0 ^= (u << 18) | (u >>> 14);
        u = (x5 + x1) | 0; x9 ^= (u << 7) | (u >>> 25);
        u = (x9 + x5) | 0; x13 ^= (u << 9) | (u >>> 23);
        u = (x13 + x9) | 0; x1 ^= (u << 13) | (u >>> 19);
        u = (x1 + x13) | 0; x5 ^= (u << 18) | (u >>> 14);
        u = (x10 + x6) | 0; x14 ^= (u << 7) | (u >>> 25);
        u = (x14 + x10) | 0; x2 ^= (u << 9) | (u >>> 23);
        u = (x2 + x14) | 0; x6 ^= (u << 13) | (u >>> 19);
        u = (x6 + x2) | 0; x10 ^= (u << 18) | (u >>> 14);
        u = (x15 + x11) | 0; x3 ^= (u << 7) | (u >>> 25);
        u = (x3 + x15) | 0; x7 ^= (u << 9) | (u >>> 23);
        u = (x7 + x3) | 0; x11 ^= (u << 13) | (u >>> 19);
        u = (x11 + x7) | 0; x15 ^= (u << 18) | (u >>> 14);

        u = (x0 + x3) | 0; x1 ^= (u << 7) | (u >>> 25);
        u = (x1 + x0) | 0; x2 ^= (u << 9) | (u >>> 23);
        u = (x2 + x1) | 0; x3 ^= (u << 13) | (u >>> 19);
        u = (x3 + x2) | 0; x0 ^= (u << 18) | (u >>> 14);
        u = (x5 + x4) | 0; x6 ^= (u << 7) | (u >>> 25);
        u = (x6 + x5) | 0; x7 ^= (u << 9) | (u >>> 23);
        u = (x7 + x6) | 0; x4 ^= (u << 13) | (u >>> 19);
        u = (x4 + x7) | 0; x5 ^= (u << 18) | (u >>> 14);
        u = (x10 + x9) | 0; x11 ^= (u << 7) | (u >>> 25);
        u = (x11 + x10) | 0; x8 ^= (u << 9) | (u >>> 23);
        u = (x8 + x11) | 0; x9 ^= (u << 13) | (u >>> 19);
        u = (x9 + x8) | 0; x10 ^= (u << 18) | (u >>> 14);
        u = (x15 + x14) | 0; x12 ^= (u << 7) | (u >>> 25);
        u = (x12 + x15) | 0; x13 ^= (u << 9) | (u >>> 23);
        u = (x13 + x12) | 0; x14 ^= (u << 13) | (u >>> 19);
        u = (x14 + x13) | 0; x15 ^= (u << 18) | (u >>> 14);
      }
      S[0] += x0; S[1] += x1; S[2] += x2; S[3] += x3; S[4] += x4; S[5] += x5; S[6] += x6; S[7] += x7;
      S[8] += x8; S[9] += x9; S[10] += x10; S[11] += x11; S[12] += x12; S[13] += x13; S[14] += x14; S[15] += x15;
      // Even pieces go to the first half, odd ones to the second.
      to = ((i >> 1) + (i & 1) * 8) * 16;
      for (k = 0; k < 16; k++) { T[to + k] = S[k]; }
    }
    X.set(T);
  }

  function bytesOf(text) {
    var out = new Uint8Array(text.length), i;
    for (i = 0; i < text.length; i++) { out[i] = text.charCodeAt(i) & 255; }
    return out;
  }
  var SALT = bytesOf("xibalba/pow-memory/1");

  var password = null, N = 0, at = 0, phase = 0;

  // start begins the calculation for one candidate. The memory is asked
  // for once and used for every candidate. It returns false if the
  // browser will not give that much.
  function start(text, n) {
    try {
      if (!V || V.length !== n * WORDS) { V = new Uint32Array(n * WORDS); }
    } catch (e) { V = null; return false; }
    password = bytesOf(text);
    N = n; at = 0; phase = 1;
    var b = pbkdf2(password, SALT, WORDS * 4), i;
    for (i = 0; i < WORDS; i++) {
      X[i] = b[4 * i] | (b[4 * i + 1] << 8) | (b[4 * i + 2] << 16) | (b[4 * i + 3] << 24);
    }
    return true;
  }

  // step works until the time "until" (as Date.now() counts) and returns
  // the value when the calculation is done, null before.
  function step(until) {
    var k, j, i;
    while (phase === 1) {
      for (k = 0; k < 64 && at < N; k++, at++) { V.set(X, at * WORDS); blockMix(); }
      if (at === N) { phase = 2; at = 0; }
      if (Date.now() >= until) { return null; }
    }
    while (phase === 2) {
      for (k = 0; k < 64 && at < N; k++, at++) {
        j = (X[WORDS - 16] & (N - 1)) * WORDS;
        for (i = 0; i < WORDS; i++) { X[i] ^= V[j + i]; }
        blockMix();
      }
      if (at === N) { phase = 3; break; }
      if (Date.now() >= until) { return null; }
    }
    if (phase !== 3) { return null; }
    phase = 0;
    var b = new Uint8Array(WORDS * 4);
    for (i = 0; i < WORDS; i++) {
      b[4 * i] = X[i] & 255; b[4 * i + 1] = (X[i] >>> 8) & 255; b[4 * i + 2] = (X[i] >>> 16) & 255; b[4 * i + 3] = X[i] >>> 24;
    }
    return pbkdf2(password, b, 32);
  }

  window.xibalbaMemory = { start: start, step: step };
})();
