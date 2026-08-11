// Minimal express server the e2e test builds and runs inside DinD. Proving it
// responds on :3000 from the HOST exercises the full port-forwarding chain:
// host:3000 -> outer sandbox container -> inner DinD container.
const express = require('express');

const app = express();
const PORT = 3000;

// The e2e assertion GETs / and checks for this exact marker.
app.get('/', (_req, res) => {
  res.status(200).send('corral e2e ok');
});

app.get('/healthz', (_req, res) => {
  res.status(200).json({ status: 'ok' });
});

app.listen(PORT, '0.0.0.0', () => {
  console.log(`corral-e2e web-app listening on :${PORT}`);
});
