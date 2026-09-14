import http from 'k6/http';
import { check } from 'k6';

const publicIPs = [
  '8.8.8.8',
  '1.1.1.1',
  '9.9.9.9',
  '208.67.222.222',
  '4.2.2.1',
  '64.6.64.6',
  '94.140.14.14',
  '185.228.168.9',
  '2001:4860:4860::8888',
  '2606:4700:4700::1111',
  '2620:fe::fe',
  '2a10:50c0::ad1:ff',
];

export const options = {
  scenarios: {
    lookup: {
      executor: 'constant-arrival-rate',
      rate: Number(__ENV.RPS || 1000),
      timeUnit: '1s',
      duration: __ENV.DURATION || '60s',
      preAllocatedVUs: Number(__ENV.VUS || 250),
      maxVUs: Number(__ENV.MAX_VUS || 2000),
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.001'],
    checks: ['rate>0.999'],
  },
};

export default function () {
  const ip = publicIPs[Math.floor(Math.random() * publicIPs.length)];
  const res = http.post(`${__ENV.BASE_URL}/v1/lookup`, JSON.stringify({ ip }), {
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${__ENV.API_TOKEN}`,
    },
  });

  const expectedApplicationStatus = res.status === 200 || res.status === 404;
  check(res, {
    'expected application status': () => expectedApplicationStatus,
    'request id returned': (r) => Boolean(r.headers['X-Request-Id']),
    'rich response has source metadata when found': (r) => {
      if (r.status !== 200) return true;
      try {
        const body = r.json();
        return Boolean(body.source?.city_database && body.source?.asn_database && body.request_id);
      } catch (_) {
        return false;
      }
    },
  });

  if (expectedApplicationStatus) {
    res.error = '';
  }
}
