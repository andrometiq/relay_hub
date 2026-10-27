#!/usr/bin/env python3
"""Destructive only to an owned, synthetic schema in the guarded local test database."""
import argparse, hashlib, json, os, subprocess, tempfile, uuid
from pathlib import Path
import psycopg2
from psycopg2 import sql


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--site-config',type=Path,required=True)
    parser.add_argument('--report',type=Path,required=True)
    args=parser.parse_args();cfg=json.loads(args.site_config.read_text())
    if cfg.get('db_name')!='relay_ai_test_20260922' or cfg.get('db_host')!='127.0.0.1' or cfg.get('db_port')!=18433:
        raise SystemExit('Only the isolated loopback test database is permitted')
    env=os.environ.copy();env.update(PGHOST='127.0.0.1',PGPORT='18433',PGDATABASE=cfg['db_name'],PGUSER=cfg.get('db_user') or cfg['db_name'],PGPASSWORD=cfg['db_password'])
    conn=psycopg2.connect(host=env['PGHOST'],port=18433,dbname=env['PGDATABASE'],user=env['PGUSER'],password=env['PGPASSWORD']);conn.autocommit=True
    schema='relay_restore_'+uuid.uuid4().hex;report={'passed':False,'scope':'synthetic schema only, not full site or file restore'}
    def fingerprint():
        with conn.cursor() as cur:
            cur.execute(sql.SQL('select id,request_key,payload,status from {}.intents order by id').format(sql.Identifier(schema)))
            rows=cur.fetchall();return len(rows),hashlib.sha256(json.dumps(rows,separators=(',',':')).encode()).hexdigest()
    try:
        with conn.cursor() as cur:
            cur.execute(sql.SQL('create schema {}').format(sql.Identifier(schema)))
            cur.execute(sql.SQL('create table {}.intents (id bigint generated always as identity primary key,request_key text unique not null,payload text not null,status text not null)').format(sql.Identifier(schema)))
            cur.execute(sql.SQL("insert into {}.intents(request_key,payload,status) select 'fixture-'||n,'Synthetic unicode தமிழ் '||n,case when n%3=0 then 'submitted' else 'queued' end from generate_series(1,1000) n").format(sql.Identifier(schema)))
        before=fingerprint()
        with tempfile.TemporaryDirectory(prefix='relay-restore-') as directory:
            archive=Path(directory)/'backup.dump'
            result=subprocess.run(['pg_dump','--format=custom','--no-owner','--no-acl','--schema='+schema,'--file='+str(archive)],env=env,capture_output=True,text=True)
            if result.returncode:raise RuntimeError('pg_dump failed: '+result.stderr[-1000:])
            archive.chmod(0o600)
            with conn.cursor() as cur:cur.execute(sql.SQL('drop schema {} cascade').format(sql.Identifier(schema)))
            result=subprocess.run(['pg_restore','--exit-on-error','--single-transaction','--no-owner','--no-acl','--dbname='+env['PGDATABASE'],str(archive)],env=env,capture_output=True,text=True)
            if result.returncode:raise RuntimeError('pg_restore failed: '+result.stderr[-1000:])
            after=fingerprint()
            if before!=after:raise RuntimeError('Restored rows or payload digest changed')
            with conn.cursor() as cur:
                cur.execute(sql.SQL("insert into {}.intents(request_key,payload,status) values ('after-restore','Recovery works','queued') returning id").format(sql.Identifier(schema)))
                if cur.fetchone()[0]!=1001:raise RuntimeError('Identity sequence was not restored')
            report.update(passed=True,rows_verified=before[0],payload_sha256=before[1],identity_sequence_verified=True,archive_bytes=archive.stat().st_size)
    finally:
        with conn.cursor() as cur:cur.execute(sql.SQL('drop schema if exists {} cascade').format(sql.Identifier(schema)))
        conn.close();args.report.parent.mkdir(parents=True,exist_ok=True);args.report.write_text(json.dumps(report,indent=2)+'\n')
    print(json.dumps(report))

if __name__=='__main__':main()
