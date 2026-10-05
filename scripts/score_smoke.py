"""Exercise score submission against the local API with an original ESE chart."""
import json
import uuid

from demo import Client, SAMPLES


def main():
    client = Client()
    user = client.auth()
    status, chart, _ = client.upload(SAMPLES[2])
    assert status == 201, (status, chart)
    try:
        payload = {
            'songId': chart['id'], 'difficulty': 'Oni',
            'good': 470, 'ok': 10, 'bad': 8, 'score': 900000, 'drumroll': 50, 'max_combo': 350,
        }
        headers = {'Idempotency-Key': str(uuid.uuid4())}
        status, score, _ = client.call('POST', '/scores', payload, headers)
        assert status == 201, (status, score)
        assert all(score[k] == value for k, value in payload.items())
        assert score['userId'] == user['id'] and score['songId'] == chart['id']
        target = next(d for d in chart['difficulties'] if d['blockIndex'] == score['blockIndex'])
        assert target['style'] == 'Single' and target['cloudScoreEligible']
        status, repeated, _ = client.call('POST', '/scores', payload, headers)
        assert status == 200 and repeated == score, (status, repeated)
        status, conflict, _ = client.call('POST', '/scores', {**payload, 'score': 900001}, headers)
        assert status == 409 and conflict['code'] == 'IDEMPOTENCY_CONFLICT'
        print('PASS authenticated score submission, Single selection and idempotent retry')
        print(json.dumps({'scoreId': score['id'], 'songId': chart['id']}, ensure_ascii=False))
    finally:
        status, _, _ = client.call('DELETE', '/charts/' + chart['id'], {})
        assert status == 200
        status, _, _ = client.call('POST', '/auth/logout', {})
        assert status == 200
    print('Test chart soft-deleted; score retained for database inspection.')


if __name__ == '__main__':
    main()
