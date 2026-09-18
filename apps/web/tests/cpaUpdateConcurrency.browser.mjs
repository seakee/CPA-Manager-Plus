// Playwright acceptance function. Start apps/web Vite on 127.0.0.1:5187,
// then pass this file to browser_run_code_unsafe. It creates and closes its
// own browser context, uses two real page realms and native Web Locks, and
// intercepts every API request. No live Manager or Runtime is contacted.
async function verifyCPAUpdateConcurrency(page) {
  const base = 'http://127.0.0.1:5187';
  const prefix = '/usage-service/runtime/updates';
  const browser = page.context().browser();
  if (!browser) throw new Error('A browser is required for independent page contexts');
  const context = await browser.newContext();
  const assert = (condition, message) => {
    if (!condition) throw new Error(message);
  };
  const barrier = () => {
    let release;
    const promise = new Promise((resolve) => {
      release = resolve;
    });
    return { promise, release };
  };
  const freshReads = barrier();
  const prepareResponse = barrier();
  const fixture = {
    calls: [],
    operations: new Map(),
    errors: [],
    freshReads: 0,
    simultaneous: false,
    status: {
      mode: 'embedded',
      state: 'update_available',
      current_version: '7.1.0',
      active_artifact_id: 'sha256:' + 'a'.repeat(64),
      target_version: '7.2.0',
      last_success_at: new Date().toISOString(),
      stale: false,
      prepare_supported: true,
      activate_supported: true,
      actionable: true,
    },
  };
  let a, b;
  let stage = 'initial status';
  const cpa = (tab) => tab.locator('section[aria-labelledby="cpa-update-title"]');
  const readRecord = (tab) => tab.evaluate(() => localStorage.getItem(window.__cpaScope));
  try {
    await context.addInitScript(() => {
      window.__cpaDeletes = 0;
      const remove = Storage.prototype.removeItem;
      Storage.prototype.removeItem = function (key) {
        if (key.startsWith('cpamp:cpa-update-intent:v1:')) window.__cpaDeletes++;
        return remove.call(this, key);
      };
    });
    await context.route('**/*', async (route) => {
      const request = route.request();
      const raw = request.url();
      if (!raw.startsWith(base + '/')) return route.abort();
      const relative = raw.slice(base.length);
      const pathname = relative.split('?')[0];
      if (!pathname.startsWith('/usage-service/') && !pathname.startsWith('/v0/management/'))
        return route.continue();
      const body = request.postData() ? request.postDataJSON() : null;
      const tab = request.frame().page() === a ? 'A' : 'B';
      const query = Object.fromEntries(
        (relative.split('?')[1] || '')
          .split('&')
          .filter(Boolean)
          .map((pair) => pair.split('=').map(decodeURIComponent))
      );
      fixture.calls.push({ tab, pathname, method: request.method(), body, query });
      const send = (data, status = 200) =>
        route
          .fulfill({ status, contentType: 'application/json', body: JSON.stringify(data) })
          .catch(() => {});
      if (pathname === '/usage-service/info')
        return send({
          service: 'cpa-manager-plus',
          mode: 'docker',
          configured: true,
          adminReady: true,
          projectInitialized: true,
          setupRequired: false,
          migrationStatus: 'ready',
          dataKeyReady: true,
        });
      if (pathname === '/usage-service/config')
        return send({
          source: 'db',
          config: {
            cpaConnection: { cpaBaseUrl: 'http://fixture.invalid', managementKeyConfigured: true },
            collector: { enabled: false },
          },
        });
      if (pathname.startsWith('/usage-service/updates'))
        return send({
          current_version: 'v2.0.0-beta.1',
          channel_preference: 'auto',
          channel: 'beta',
          automatic: false,
          state: 'up_to_date',
          stale: false,
          last_success_at: new Date().toISOString(),
        });
      if (pathname === prefix) {
        if (fixture.simultaneous && fixture.freshReads < 2) {
          fixture.freshReads++;
          if (fixture.freshReads === 2) freshReads.release();
          await freshReads.promise;
        }
        return send(fixture.status);
      }
      if (pathname.startsWith(prefix + '/operations/')) {
        const phase = pathname.split('/').at(-1);
        const state = fixture.operations.get(phase + ':' + query.request_id) || 'not_found';
        return send({
          phase,
          ...query,
          runtime_operation_id: phase + ':' + query.request_id,
          state,
        });
      }
      if (pathname === prefix + '/prepare' || pathname === prefix + '/activate') {
        const phase = pathname.split('/').at(-1);
        const record = JSON.parse(await readRecord(request.frame().page()));
        assert(
          record.request_id === body.request_id && record.phase === phase,
          'Mutation preceded durable intent'
        );
        assert(body.target_version === '7.2.0', 'Noncanonical target');
        fixture.operations.set(phase + ':' + body.request_id, 'running');
        if (phase === 'activate') return route.abort('failed');
        await prepareResponse.promise;
        fixture.operations.set(phase + ':' + body.request_id, 'succeeded');
        return send({
          phase,
          ...body,
          runtime_operation_id: phase + ':' + body.request_id,
          state: 'succeeded',
          already_applied: false,
        });
      }
      if (pathname === prefix + '/check') throw new Error('Unexpected CPA check');
      return send({});
    });
    a = await context.newPage();
    b = await context.newPage();
    const clients = [a, b];
    for (const tab of clients) {
      tab.on('pageerror', (error) => fixture.errors.push(error.message));
      await tab.goto(base + '/#/system/updates');
      await tab.evaluate(async (base) => {
        const { useAuthStore } = await import('/src/stores/useAuthStore.ts');
        const { useLanguageStore } = await import('/src/stores/useLanguageStore.ts');
        const { cpaUpdateStorageKey } =
          await import('/src/features/system/cpaUpdateIntentStorage.ts');
        await useAuthStore.getState().login({
          apiBase: base,
          managementKey: 'isolated-cpa-concurrency-key',
          rememberPassword: true,
          sessionMode: 'manager_embedded',
          sessionPanelBase: base,
        });
        useLanguageStore.getState().setLanguage('en');
        window.__cpaScope = cpaUpdateStorageKey(base, 'isolated-cpa-concurrency-key');
        location.hash = '/system/updates';
      }, base);
      await cpa(tab).getByRole('button', { name: 'Prepare CPA update', exact: true }).waitFor();
      assert(
        await cpa(tab).getByRole('button', { name: 'Prepare CPA update', exact: true }).isEnabled(),
        'Initial prepare disabled'
      );
      assert((await readRecord(tab)) === null, 'A tab did not initially see no intent');
    }
    stage = 'simultaneous claim';
    const scope = await a.evaluate(() => window.__cpaScope);
    await a.evaluate(async () => {
      await new Promise((resolve) => {
        navigator.locks.request(
          window.__cpaScope,
          () =>
            new Promise((release) => {
              window.__releaseClaimLock = release;
              resolve();
            })
        );
      });
    });
    fixture.simultaneous = true;
    await Promise.all(
      clients.map((tab) =>
        cpa(tab).getByRole('button', { name: 'Prepare CPA update', exact: true }).click()
      )
    );
    await a.waitForFunction(
      async (scope) =>
        (await navigator.locks.query()).pending.filter((lock) => lock.name === scope).length === 2,
      scope
    );
    const mutations = () =>
      fixture.calls.filter(
        (call) => call.method === 'POST' && /\/(prepare|activate)$/.test(call.pathname)
      );
    assert(
      mutations().length === 0 && (await readRecord(a)) === null,
      'Mutation or intent escaped the held claim lock'
    );
    await a.evaluate(() => window.__releaseClaimLock());
    await a.waitForFunction(() => localStorage.getItem(window.__cpaScope) !== null);
    const winner = JSON.parse(await readRecord(a));
    // The loser must observe the winner while its mutation response is still pending.
    const observationDeadline = Date.now() + 10000;
    while (
      !fixture.calls.some((call) => call.pathname === prefix + '/operations/prepare') &&
      Date.now() < observationDeadline
    )
      await a.waitForTimeout(20);
    assert(
      fixture.calls.some((call) => call.pathname === prefix + '/operations/prepare'),
      'Loser did not query the winner'
    );
    assert(mutations().length === 1, 'Simultaneous claims submitted multiple new operations');
    assert(
      mutations()[0].body.request_id === winner.request_id,
      'Winning record and mutation differ'
    );
    const loser = mutations()[0].tab === 'A' ? b : a;
    // A loser may observe not_found before the winner's POST reaches Runtime.
    // Requery explicitly; it must never submit on the winner's behalf.
    await cpa(loser).getByRole('button', { name: 'Query again', exact: true }).click();
    await cpa(loser).getByText('Running; observing progress.', { exact: true }).waitFor();
    const observed = fixture.calls.find((call) => call.pathname === prefix + '/operations/prepare');
    assert(observed.query.request_id === winner.request_id, 'Loser did not adopt the winner');
    stage = 'prepare completion';
    prepareResponse.release();
    for (const tab of clients) await cpa(tab).getByText('Prepared', { exact: true }).waitFor();
    const preparedRaw = await readRecord(a);
    await a.waitForTimeout(200);
    assert((await readRecord(a)) === preparedRaw, 'Read-only recovery rewrote prepared revision');

    stage = 'stale prepared Forget';
    await cpa(a).getByRole('button', { name: 'Forget recovery record', exact: true }).click();
    await cpa(b).getByRole('button', { name: 'Activate CPA update', exact: true }).click();
    await b.getByRole('button', { name: 'Confirm activation', exact: true }).click();
    await cpa(b).getByText('Running; observing progress.', { exact: true }).waitFor();
    await cpa(a)
      .getByText(/^(Running; observing progress\.|No durable operation observed\.)/)
      .waitFor();
    const activatingRaw = await readRecord(b);
    const activation = JSON.parse(activatingRaw);
    assert(
      activation.phase === 'activate' && activation.request_id === winner.request_id,
      'Activation lost flow identity'
    );
    const deletesBefore = await a.evaluate(() => window.__cpaDeletes);
    await a.getByRole('button', { name: 'Forget record', exact: true }).click();
    await cpa(a)
      .getByText(
        'The recovery record changed in another context. Submissions are blocked; query again to inspect the operation.',
        { exact: true }
      )
      .waitFor();
    assert(
      (await a.evaluate(() => window.__cpaDeletes)) === deletesBefore,
      'Stale Forget called removeItem'
    );
    assert((await readRecord(a)) === activatingRaw, 'Stale Forget removed the new activation');

    // Simulate a corrupted browser record, then another tab restoring its known
    // valid activation record under the same coordination lock.
    stage = 'stale corrupt Forget';
    await a.evaluate(async () => {
      await navigator.locks.request(window.__cpaScope, () =>
        localStorage.setItem(window.__cpaScope, '{corrupt')
      );
    });
    await a.reload();
    await a.evaluate((scope) => {
      window.__cpaScope = scope;
    }, scope);
    await cpa(a)
      .getByText(
        'The recovery record is invalid. Submissions are blocked; the record has been kept for explicit recovery or removal.',
        { exact: true }
      )
      .waitFor();
    await cpa(a).getByRole('button', { name: 'Forget recovery record', exact: true }).click();
    await b.evaluate(async (raw) => {
      await navigator.locks.request(window.__cpaScope, () =>
        localStorage.setItem(window.__cpaScope, raw)
      );
    }, activatingRaw);
    await cpa(a).getByText('Running; observing progress.', { exact: true }).waitFor();
    const corruptDeletes = await a.evaluate(() => window.__cpaDeletes);
    await a.getByRole('button', { name: 'Forget record', exact: true }).click();
    await cpa(a)
      .getByText(
        'The recovery record changed in another context. Submissions are blocked; query again to inspect the operation.',
        { exact: true }
      )
      .waitFor();
    assert(
      (await a.evaluate(() => window.__cpaDeletes)) === corruptDeletes,
      'Corrupt-record confirmation deleted a replacement'
    );
    assert(
      (await readRecord(a)) === activatingRaw,
      'Corrupt-record confirmation lost activation correlation'
    );
    assert(mutations().length === 2, 'Recovery resubmitted an operation');
    assert(fixture.errors.length === 0, fixture.errors.join('\n'));
    return {
      simultaneousClaim: {
        pageContexts: 2,
        queuedNativeLocks: 2,
        newPreparePOSTs: 1,
        winnerObservedByLoser: true,
      },
      staleForget: { preparedToActivate: 'zero delete', corruptToValid: 'zero delete' },
      prepareActivateSameRequestID: true,
      preparedRevisionStable: true,
      pageErrors: fixture.errors,
    };
  } catch (error) {
    throw new Error(
      stage +
        ': ' +
        error.message +
        '\n' +
        JSON.stringify({
          a: a && !a.isClosed() ? await a.locator('body').innerText() : '',
          b: b && !b.isClosed() ? await b.locator('body').innerText() : '',
          calls: fixture.calls.filter((call) => call.pathname.startsWith(prefix)),
        })
    );
  } finally {
    freshReads.release();
    prepareResponse.release();
    await context.close();
  }
}
