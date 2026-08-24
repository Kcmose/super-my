import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import {
  auditSummaryText,
  cloneAgentSettings,
  collectCursorPages,
  createDefaultAgentSettings,
  normalizeNodePayload,
  normalizeUserPayload,
  validateAuditWindow,
  validateNodePayload,
  validateUserPayload,
} from '../src/utils/admin.js'
import { userRoleLabel } from '../src/utils/roles.js'

test('cursor collection is bounded and rejects repeated cursors', async () => {
  let calls = 0
  const items = await collectCursorPages(async (cursor) => {
    calls += 1
    return cursor
      ? { nodes: [{ node_id: 'two' }], next_cursor: null }
      : { nodes: [{ node_id: 'one' }], next_cursor: 'next' }
  }, 'nodes')
  assert.deepEqual(items.map((item) => item.node_id), ['one', 'two'])
  assert.equal(calls, 2)

  await assert.rejects(
    collectCursorPages(async () => ({ nodes: [], next_cursor: 'same' }), 'nodes'),
    /重复分页游标/,
  )
  await assert.rejects(
    collectCursorPages(async (_, index = 0) => ({ nodes: [], next_cursor: `page-${index}` }), 'nodes', 1),
    /安全上限/,
  )
})

function nodePayload(agentSettings) {
  return normalizeNodePayload({
    display_name: 'Edge node',
    enabled: true,
    country_code: 'hk',
    region_key: '',
    location: '',
    agent_settings: agentSettings,
  })
}

test('node forms normalize metadata and always send complete default Agent settings', () => {
  const payload = normalizeNodePayload({
    display_name: '  Hong Kong Edge  ',
    enabled: true,
    country_code: 'hk',
    region_key: '  hk-edge ',
    location: '   ',
  })
  assert.deepEqual(payload, {
    display_name: 'Hong Kong Edge',
    enabled: true,
    country_code: 'HK',
    region_key: 'hk-edge',
    location: null,
    agent_settings: {
      metrics: {
        collect_interval_seconds: 5,
        report_interval_seconds: 10,
        mountpoints: ['/'],
        include_virtual_interfaces: false,
      },
      agent: {
        config_refresh_interval_seconds: 60,
        max_memory_queue_seconds: 300,
      },
      limits: {
        max_batch_samples: 120,
      },
    },
  })
  assert.equal(validateNodePayload(payload), '')
  assert.match(validateNodePayload({ ...payload, country_code: 'HKG' }), /两个大写英文字母/)
})

test('editing clones every Agent setting without sharing nested state', async () => {
  const sourceSettings = {
    metrics: {
      collect_interval_seconds: 15,
      report_interval_seconds: 30,
      mountpoints: ['/', '/data'],
      include_virtual_interfaces: true,
    },
    agent: {
      config_refresh_interval_seconds: 120,
      max_memory_queue_seconds: 180,
    },
    limits: {
      max_batch_samples: 42,
    },
  }
  const cloned = cloneAgentSettings(sourceSettings)
  assert.deepEqual(cloned, sourceSettings)
  assert.notEqual(cloned, sourceSettings)
  assert.notEqual(cloned.metrics, sourceSettings.metrics)
  assert.notEqual(cloned.metrics.mountpoints, sourceSettings.metrics.mountpoints)
  cloned.metrics.mountpoints.push('/scratch')
  assert.deepEqual(sourceSettings.metrics.mountpoints, ['/', '/data'])
  assert.deepEqual(nodePayload(cloneAgentSettings(sourceSettings)).agent_settings, sourceSettings)

  const component = await readFile(new URL('../src/views/admin/NodeTokens.vue', import.meta.url), 'utf8')
  assert.match(component, /agent_settings:\s*cloneAgentSettings\(node\.agent_settings\)/)
})

test('Agent setting validation accepts all lower and upper boundaries', () => {
  const minimums = createDefaultAgentSettings()
  minimums.metrics.report_interval_seconds = 5
  minimums.agent.config_refresh_interval_seconds = 10
  minimums.agent.max_memory_queue_seconds = 5
  minimums.limits.max_batch_samples = 1
  assert.equal(validateNodePayload(nodePayload(minimums)), '')

  const maximums = createDefaultAgentSettings()
  maximums.metrics.collect_interval_seconds = 300
  maximums.metrics.report_interval_seconds = 300
  maximums.metrics.mountpoints = Array.from({ length: 32 }, (_, index) => index === 0 ? '/' : `/mnt/${index}`)
  maximums.agent.config_refresh_interval_seconds = 86400
  maximums.agent.max_memory_queue_seconds = 300
  maximums.limits.max_batch_samples = 120
  assert.equal(validateNodePayload(nodePayload(maximums)), '')

  const invalidCases = [
    [(settings) => { settings.metrics.collect_interval_seconds = 4 }, /采集周期/],
    [(settings) => { settings.metrics.collect_interval_seconds = 301 }, /采集周期/],
    [(settings) => { settings.metrics.collect_interval_seconds = 11 }, /上报周期不能短于采集周期/],
    [(settings) => { settings.metrics.report_interval_seconds = 301 }, /上报周期/],
    [(settings) => { settings.agent.config_refresh_interval_seconds = 9 }, /配置刷新周期/],
    [(settings) => { settings.agent.config_refresh_interval_seconds = 86401 }, /配置刷新周期/],
    [(settings) => { settings.agent.max_memory_queue_seconds = 9 }, /内存队列时长不能短于上报周期/],
    [(settings) => { settings.agent.max_memory_queue_seconds = 301 }, /内存队列时长/],
    [(settings) => { settings.limits.max_batch_samples = 0 }, /单批样本上限/],
    [(settings) => { settings.limits.max_batch_samples = 121 }, /单批样本上限/],
  ]
  for (const [mutate, expected] of invalidCases) {
    const settings = createDefaultAgentSettings()
    mutate(settings)
    assert.match(validateNodePayload(nodePayload(settings)), expected)
  }
})

test('Agent mountpoints require root, unique absolute paths and no control characters', () => {
  const cases = [
    [[], /数量必须为 1 - 32/],
    [Array.from({ length: 33 }, (_, index) => index === 0 ? '/' : `/mnt/${index}`), /数量必须为 1 - 32/],
    [['/data'], /必须包含根目录/],
    [[' / ', '/'], /不能重复/],
    [['/', 'data'], /绝对路径/],
    [['/', '/bad\tpath'], /控制字符/],
  ]
  for (const [mountpoints, expected] of cases) {
    const settings = createDefaultAgentSettings()
    settings.metrics.mountpoints = mountpoints
    assert.match(validateNodePayload(nodePayload(settings)), expected)
  }
})

test('administrator forms force the admin role and enforce UTF-8 password length', () => {
  const create = normalizeUserPayload({ username: ' admin-two ', password: '十二字节密码', enabled: true })
  assert.equal(create.username, 'admin-two')
  assert.equal(create.role, 'admin')
  assert.equal(validateUserPayload(create), '')

  const edit = normalizeUserPayload({ username: 'admin-three', password: '', role: 'viewer', enabled: false }, true)
  assert.equal(Object.hasOwn(edit, 'password'), false)
  assert.equal(edit.role, 'admin')
  assert.equal(validateUserPayload(edit, true), '')
  assert.match(validateUserPayload({ ...edit, password: 'short' }, true), /至少需要 12/)
  assert.match(validateUserPayload({ ...edit, password: '密'.repeat(400) }, true), /最长 1024 个 UTF-8 字节/)
  assert.match(validateUserPayload({ ...edit, role: 'viewer' }, true), /管理员账号/)
})

test('legacy role values have safe user-facing labels', async () => {
  assert.equal(userRoleLabel('viewer'), '已废弃角色')
  assert.equal(userRoleLabel('admin'), '管理员')
  assert.equal(userRoleLabel('operator'), '未知角色')

  const usersComponent = await readFile(new URL('../src/views/admin/Users.vue', import.meta.url), 'utf8')
  const headerComponent = await readFile(new URL('../src/components/PanelHeader.vue', import.meta.url), 'utf8')
  assert.match(usersComponent, /response\.users\.filter\(\(user\) => user\?\.role === 'admin'\)/)
  assert.match(headerComponent, /\{\{ authStore\.currentUsername \}\}/)
  assert.match(headerComponent, />管理员<\/span>/)
  assert.match(headerComponent, /<header v-if="authStore\.isAdmin"/)
  assert.doesNotMatch(usersComponent, /<option[^>]+value="viewer"/)
  assert.doesNotMatch(usersComponent, /viewer \/ admin|viewer（只读）|admin（管理）/)
})

test('audit filters validate ordering and summaries remain plain text', () => {
  assert.match(validateAuditWindow('2026-08-22T12:00', '2026-08-22T11:00').error, /开始时间必须早于/)
  assert.match(validateAuditWindow('2026-08-22T12:00', '2026-08-22T12:00').error, /开始时间必须早于/)
  assert.equal(validateAuditWindow('', '').error, '')
  assert.equal(auditSummaryText({ value: '<strong>plain text</strong>' }), '{\n  "value": "<strong>plain text</strong>"\n}')
  assert.equal(auditSummaryText({ role: 'viewer' }), '{\n  "role": "已废弃角色"\n}')
})

test('one-time Agent credentials are never persisted or logged by the component', async () => {
  const source = await readFile(new URL('../src/views/admin/NodeTokens.vue', import.meta.url), 'utf8')
  assert.doesNotMatch(source, /localStorage|sessionStorage|console\./)
  assert.match(source, /secretDialog\.value = null/)
  assert.match(source, /onUnmounted\(\(\) =>/)
  assert.match(source, /const createdNode = await adminApi\.createNode\(payload\)/)
  assert.match(source, /adminApi\.createEnrollmentToken\(createdNode\.node_id, \{ expiresInSeconds: 900 \}\)/)
  assert.match(source, /response\?\.install_command/)
  assert.match(source, /Shell 历史/)
	assert.match(source, /sudo -i/)
	assert.match(source, /当前 Agent 凭证会立即失效/)
	assert.match(source, /节点当前已禁用，请先编辑并启用节点/)
	assert.match(source, />关闭显示<\/button>/)
  assert.match(source, /节点已经创建，但安装命令生成失败/)
	assert.equal(source.match(/adminApi\.updateNode\(/g)?.length, 1)
  assert.doesNotMatch(source, /showSecret\(\{ title: '一次性 Agent 注册令牌'/)
})
