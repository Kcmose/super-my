import test from 'node:test'
import assert from 'node:assert/strict'
import { formatBytes, normalizeDisks, normalizeNode, statusMeta, totalTraffic, usagePercent } from '../src/utils/panel.js'

test('missing metrics stay visibly unknown instead of becoming fake zero values', () => {
  const node = normalizeNode({ node_id: 'abc', display_name: 'edge', hostname: 'agent-edge-01', status: 'unregistered', country_code: 'US' })
  assert.equal(node.current_metrics, null)
  assert.equal(node.uptime_formatted, '—')
  assert.equal(formatBytes(null), '—')
  assert.equal(statusMeta(node.status).label, '未注册')
  assert.equal(node.region_name, '美国')
  assert.equal(node.hostname, 'agent-edge-01')
  assert.equal(node.display_name, 'edge')
})

test('traffic is derived only from real cumulative counters', () => {
  assert.equal(totalTraffic({ network_rx_bytes: 10, network_tx_bytes: 20 }), 30)
  assert.equal(totalTraffic({}), null)
  assert.equal(totalTraffic(null), null)
})

test('disk responses keep current snapshots and calculate bounded usage', () => {
  const disks = normalizeDisks({ disks: [{ mountpoint: '/', current: { used_bytes: 75, total_bytes: 100 }, points: [] }] })
  assert.equal(disks['/'].usage_percent, 75)
  assert.equal(usagePercent(200, 100), 100)
  assert.equal(usagePercent(0, 0), null)
})

test('node overview projects the real root disk before opening the detail drawer', () => {
  const node = normalizeNode({
    node_id: 'abc',
    display_name: 'edge',
    root_disk: { mountpoint: '/', used_bytes: 25, total_bytes: 100, available_bytes: 75 },
  })
  assert.equal(node.current_disks['/'].usage_percent, 25)

  const refreshed = normalizeNode({
    ...node,
    root_disk: { mountpoint: '/', used_bytes: 40, total_bytes: 100, available_bytes: 60 },
  }, { '/': { mountpoint: '/', usage_percent: 30 }, '/data': { mountpoint: '/data', usage_percent: 10 } })
  assert.equal(refreshed.current_disks['/'].usage_percent, 40)
  assert.equal(refreshed.current_disks['/data'].usage_percent, 10)
})
