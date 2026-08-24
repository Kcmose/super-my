import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { systemDatabaseLabel, systemHeaderState, systemOverallLabel, systemOverallTone } from '../src/utils/systemStatus.js'

test('system status remains an administrator-only read-only view', async () => {
  const [view, router, navigation, adminApi] = await Promise.all([
    readFile(new URL('../src/views/admin/SystemStatus.vue', import.meta.url), 'utf8'),
    readFile(new URL('../src/router/index.js', import.meta.url), 'utf8'),
    readFile(new URL('../src/components/AdminNav.vue', import.meta.url), 'utf8'),
    readFile(new URL('../src/api/admin.js', import.meta.url), 'utf8'),
  ])

  assert.match(router, /path:\s*'\/admin\/system'/)
  assert.match(router, /name:\s*'SystemStatus'/)
  assert.match(view, /adminApi\.getSystemStatus/)
  assert.match(view, /系统只读状态/)
  assert.match(view, /不提供服务控制、环境变量或凭据信息/)
  assert.match(view, /认证前数据库故障会由认证层返回通用失败/)
  assert.match(view, /不代表对 Nginx 入口拓扑的运行时验证/)
  assert.match(navigation, /系统只读状态/)
  assert.match(adminApi, /getSystemStatus[\s\S]*\/api\/v1\/admin\/system\/status/)
  assert.doesNotMatch(view, /(?:start|stop|restart|reload|exec|shell|terminal|webssh)Service/i)
  assert.doesNotMatch(view, /password|database_url|connection_string/i)
  assert.doesNotMatch(view, /agent_api_separated|Agent API 入口|public_api_enabled|公共 API/)
  assert.match(view, /overallTone === 'error' \? 'bg-rose-400'/)
  assert.match(view, /overallTone === 'error' \? 'text-rose-400'/)
})

test('first status failure is not rendered as an in-progress read', () => {
  const errorMessage = '系统状态读取失败'
  assert.deepEqual(systemHeaderState(null, false, errorMessage), {
    label: '系统状态读取失败',
    tone: 'error',
  })
  assert.equal(systemOverallLabel(null, false, errorMessage), '读取失败')
  assert.equal(systemOverallTone(null, errorMessage), 'error')
  assert.equal(systemDatabaseLabel(null, false, errorMessage), '未知')

  assert.equal(systemHeaderState(null, true, '').label, '正在读取系统状态')
  assert.equal(systemDatabaseLabel(null, true, ''), '读取中')
})

test('refresh failure keeps the last successfully loaded status visible', () => {
  const previous = { status: 'ready', database: { status: 'ready' } }
  assert.equal(systemHeaderState(previous, false, '刷新失败').label, 'API 与数据库就绪')
  assert.equal(systemOverallLabel(previous, false, '刷新失败'), '正常')
  assert.equal(systemOverallTone(previous, '刷新失败'), 'ready')
  assert.equal(systemDatabaseLabel(previous, false, '刷新失败'), '就绪')
  assert.equal(systemOverallTone({ status: 'degraded' }, '刷新失败'), 'degraded')
})

test('login access display is populated only by the server endpoint', async () => {
  const [view, authApi] = await Promise.all([
    readFile(new URL('../src/views/Login.vue', import.meta.url), 'utf8'),
    readFile(new URL('../src/api/auth.js', import.meta.url), 'utf8'),
  ])

  assert.match(authApi, /getAccessStatus[\s\S]*\/api\/v1\/auth\/access/)
  assert.match(view, /response\?\.allowed !== true/)
  assert.match(view, /response\?\.source_ip/)
  assert.match(view, /allowed=true/)
  assert.doesNotMatch(view, /X-Forwarded-For|Forwarded|X-Probe-Client-IP/)
})
