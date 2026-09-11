import http from 'k6/http';
import { check } from 'k6';

export const options = {
  scenarios: {
    lookup: {
      executor: 'constant-arrival-rate',
      rate: Number(__ENV.RPS || 10000),
      timeUnit: '1s',
      duration: __ENV.DURATION || '60s',
      preAllocatedVUs: Number(__ENV.VUS || 500),
      maxVUs: Number(__ENV.MAX_VUS || 2000),
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.001'],
    http_req_duration: ['p(95)<100', 'p(99)<250'],
  },
};

export default function () {
  const res = http.post(`${__ENV.BASE_URL}/v1/country`, JSON.stringify({ ip: '8.8.8.8' }), {
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${__ENV.API_TOKEN}` },
  });
  check(res, { 'status 200': (r) => r.status === 200 });
}
