"""Keep the archive controls provided by the current download API."""
from playwright.sync_api import expect


def test_archive_jobs_and_vault_setting_survive_react_migration(page, app):
    writes=[]
    settings={'extract_archives':False,'import_mode':'move','vault_archive_enabled':False}
    download={'type':'archive','title':'Archive fixture','hash':'archive-hash','status':'downloading','files':[
        {'title':'Finished ROM','job_id':'done-job','status':'completed','progress':100},
        {'title':'Failed ROM','job_id':'failed-job','status':'error','error':'Fixture failure','can_retry':True},
    ]}
    reads=[]
    def route(request):
        path=request.request.url.split('/api',1)[1]
        method=request.request.method
        if method!='GET':writes.append((method,path,request.request.post_data_json if request.request.post_data else None))
        if path=='/auth/status':data={'auth_required':False,'authenticated':False}
        elif path=='/downloads':
            reads.append(1);data={'downloads':[download]}
        elif path=='/settings':
            if method=='PUT':settings.update(request.request.post_data_json)
            data=settings
        elif path.endswith('/retry'):data={'success':True,'message':'Retrying (#2)'}
        else:data={'success':True}
        request.fulfill(json=data)
    page.route('**/api/**',route)
    page.goto(app['base'])
    page.locator('[data-tab="downloads"]').click()
    details=page.locator('details[data-archive-hash="archive-hash"]')
    expect(details).to_contain_text('Finished ROM')
    expect(details).to_contain_text('Fixture failure')
    details.get_by_role('button',name='Retry').click()
    expect(page.locator('[aria-live=polite]')).to_contain_text('Retrying (#2)')
    details.locator('summary').click()
    expect(details).not_to_have_attribute('open','')
    # A real polling refresh must not reopen the file list.
    before=len(reads)
    page.wait_for_timeout(5500)
    assert len(reads)>before
    expect(details).not_to_have_attribute('open','')
    page.get_by_role('button',name='Remove',exact=True).click()
    expect(page.locator('[aria-live=polite]')).to_contain_text('Removed download')
    for path in ['/downloads/torrent/archive-hash','/downloads/done-job','/downloads/failed-job']:
        assert ('DELETE',path,None) in writes
    page.locator('[data-tab="settings"]').click()
    with page.expect_request(lambda request: request.method == 'PUT' and request.url.endswith('/api/settings')) as saved:
        page.locator('#setting-vault-archive').check()
    expect(page.locator('#setting-vault-archive')).to_be_checked()
    assert saved.value.post_data_json['vault_archive_enabled'] is True
