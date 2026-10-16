// M0-6 的截图清单（存储配置）：npm run screenshots -- --spec scripts/shots/m0-storage.mjs
// 前提：server 连的库里有默认存储（首次启动时按 S3_* 登记的，桶 doclite；M7-8 起按"默认"标记找，不认名字——
// 开发库的默认存储已改名为"SeaweedFS（本机 Docker）"），它指向的 SeaweedFS 在运行（测试连接要真正访问它）。管理员账号同 m0.mjs：
// INIT_ADMIN_USERNAME / INIT_ADMIN_PASSWORD，默认 admin / change-me-now，也可以用 --account 覆盖。
//
// 截图不保存任何修改：失败的那张只是在编辑表单里改了桶名再测试，不点保存。

const admin = process.env.INIT_ADMIN_USERNAME ?? 'admin'
const adminPassword = process.env.INIT_ADMIN_PASSWORD ?? 'change-me-now'

/** 默认存储的卡片（带"默认"标记的那一张）。 */
const CARD = '[data-testid=storage-card]:has([data-testid=default-badge])'

/** 打开默认存储的编辑表单。 */
async function editDefault(page) {
  await page.click(`${CARD} [data-testid=storage-edit]`)
  await page.waitForFunction(() => document.querySelector('#sf-bucket')?.value === 'doclite')
}

/** 点"测试连接"，等到出结果。 */
async function runTest(page) {
  await page.click('[data-testid=storage-test]')
  await page.waitForSelector('[data-testid=storage-form] [data-testid=storage-test-result]', { timeout: 30000 })
}

export default {
  out: '../docs/screenshots/m0',
  accounts: {
    [admin]: adminPassword,
  },
  shots: [
    // 列表页：默认存储、状态、地址、桶、前缀；右边是新增表单。
    { name: 'admin-storage', user: admin, path: '/system/storage', waitFor: CARD },

    // 编辑默认存储，测试连接成功（含工作站地址的检查，如果配置了和 server 不同的地址）。
    {
      name: 'admin-storage-test-ok',
      user: admin,
      path: '/system/storage',
      waitFor: CARD,
      async prepare(page) {
        await editDefault(page)
        await runTest(page)
        await page.waitForSelector('[data-testid=storage-test-result][data-result=ok]')
      },
    },

    // 编辑表单里把桶名改错（不保存）再测试：桶不存在。
    {
      name: 'admin-storage-test-failed',
      user: admin,
      path: '/system/storage',
      waitFor: CARD,
      async prepare(page) {
        await editDefault(page)
        await page.click('#sf-bucket', { count: 3 })
        await page.type('#sf-bucket', 'doclite-typo')
        await runTest(page)
        await page.waitForSelector('[data-testid=storage-test-result][data-result=bucket_not_found]')
      },
    },
  ],
}
