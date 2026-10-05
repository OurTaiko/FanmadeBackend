"""Check localization and owner edits using a real ESE upload; originals stay intact."""
import io
import uuid
import zipfile
from urllib.parse import quote

from demo import Client, ESE, SAMPLES


def main():
    owner, other = Client(), Client()
    owner.auth()
    other.auth('FANMADE_OTHER_SESSION_COOKIE')
    rel = SAMPLES[0]
    original = ESE / rel
    fields = {}
    for raw in original.read_text(encoding='utf-8-sig').splitlines():
        key, sep, value = raw.split('//', 1)[0].strip().partition(':')
        if sep and (key.startswith('TITLE') or key.startswith('SUBTITLE')):
            fields[key] = value.strip()
    status, chart, _ = owner.upload(rel)
    assert status == 201, (status, chart)
    path = '/charts/' + chart['id']
    try:
        assert chart['title'] == fields['TITLE'] and chart['subtitle'] == fields['SUBTITLE']
        for suffix, locale in [('JA', 'ja'), ('ZH', 'zh'), ('KO', 'ko')]:
            if 'TITLE' + suffix in fields:
                assert chart['titleTranslations'][locale] == fields['TITLE' + suffix]
            if 'SUBTITLE' + suffix in fields:
                assert chart['subtitleTranslations'][locale] == fields['SUBTITLE' + suffix]
        payload = dict(songId=chart['id'], difficulty='Oni', good=300, ok=20, bad=11, score=900000, drumroll=50, max_combo=250)
        key = {'Idempotency-Key': str(uuid.uuid4())}
        status, score, _ = owner.call('POST', '/scores', payload, key)
        assert status == 201, (status, score)
        for client, headers, expected in [(other, {}, 403), (owner, {'X-CSRF-Token': ''}, 403)]:
            status, _, _ = client.call('PATCH', path, {'title': 'Denied'}, headers)
            assert status == expected
        for patch in [{'title': ''}, {'titleTranslations': {'xx': 'Unknown'}}, {'subtitle': 1}]:
            status, _, _ = owner.call('PATCH', path, patch)
            assert status == 422
        status, _, _ = owner.call('PATCH', path, {'ownerId': 'forged'})
        assert status == 400
        marker = '测试新歌名' + uuid.uuid4().hex[:8]
        patch = {'title': 'Edited English', 'subtitle': '', 'titleTranslations': {'ja': '編集後', 'zh': marker}, 'subtitleTranslations': {'ko': '부제'}}
        status, updated, _ = owner.call('PATCH', path, patch)
        assert status == 200, (status, updated)
        assert updated['title'] == 'Edited English' and updated['subtitle'] == ''
        assert updated['titleTranslations']['zh'] == marker and updated['subtitleTranslations']['ko'] == '부제'
        for k in ['id', 'tjaHash', 'audioHash', 'difficulties']:
            assert updated[k] == chart[k], k
        status, detail, _ = owner.call('GET', path)
        assert status == 200 and detail == updated
        for endpoint in ['/charts?q=', '/me/charts?q=']:
            status, result, _ = owner.call('GET', endpoint + quote(marker))
            assert status == 200 and result['total'] == 1 and result['items'][0]['id'] == chart['id']
        status, repeated, _ = owner.call('POST', '/scores', payload, key)
        assert status == 200 and repeated == score
        status, archive, _ = owner.call('GET', path + '/download')
        assert status == 200
        with zipfile.ZipFile(io.BytesIO(archive)) as z:
            assert z.read(original.name) == original.read_bytes()
            assert z.read(chart['wave']) == (original.parent / chart['wave']).read_bytes()
        status, partial, _ = owner.call('PATCH', path, {'titleTranslations': {'zh': None}})
        assert status == 200 and partial['titleTranslations'].get('zh') == chart['titleTranslations'].get('zh')
        assert partial['titleTranslations']['ja'] == '編集後'
        status, reset, _ = owner.call('PATCH', path, {'title': None, 'subtitle': None, 'titleTranslations': None, 'subtitleTranslations': None})
        assert status == 200 and reset == chart
        print('PASS localized upload, owner/CSRF validation, partial edits/reset, multilingual search, unchanged score and ZIP')
        print('Verified chart:', chart['id'], 'score:', score['id'])
    finally:
        status, _, _ = owner.call('DELETE', path, {})
        assert status == 200
    status, _, _ = owner.call('PATCH', path, {'title': 'Deleted'})
    assert status == 404
    print('PASS edits rejected after deletion; test chart soft-deleted')


if __name__ == '__main__':
    main()
