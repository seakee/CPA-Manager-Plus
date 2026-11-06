"""Plan #64, exclusively disposable official CPA, using the original S1 owner.

No independent config writer or secret persistence. Per-step authority is issued
by trusted Host only after rechecking all mounts, namespace consumers, ports,
source bytes and the current generation. Existing task approval is required by
the entrypoint; the environment acknowledgement is not itself authority.
"""
import hashlib
import json
import os
import secrets
import time

from fresh_s2a_proof import BODY, counts


def exercise(command,http,record,evidence,name,probe,runtime,old,old_id,admin,api,permit,volumes,containers,images,namespace_owner,out):
    def call(resource,method="GET",body=None,key=admin):
        return http(28471,api+resource,method,body,key)

    def check(label,key,want=503,arrivals=0):
        before=counts(command,runtime);upstream=len(http(18321,"/events")[1])
        code,_=http(28471,"/v1/chat/completions","POST",BODY,key)
        after=counts(command,runtime);delta=len(http(18321,"/events")[1])-upstream
        assert code==want and delta==arrivals and after["cpaChat"]-before["cpaChat"]==arrivals,(label,code,delta)
        record(scenario=label,status=code,cpaChatArrivals=arrivals,upstreamArrivals=delta)

    def grant(operation,step):
        # The trusted Host owns this unique project, credentials and every
        # volume/namespace consumer throughout the serial short grant window.
        live=json.loads(command("docker","inspect",*command("docker","ps","-aq").splitlines()))
        runtime_info=next(v for v in live if v["Name"]=="/"+runtime)
        for obj in live:
            trusted=obj["Name"].lstrip("/") in containers
            mounts={m["Name"] for m in obj.get("Mounts",[]) if m["Type"]=="volume"}
            if mounts.intersection(volumes) or namespace_owner(obj)==runtime_info["Id"]:
                assert trusted,"unknown source/namespace writer; STOP"
        for obj in (o for o in live if o["Name"].lstrip("/") in containers):
            assert not obj["HostConfig"]["Privileged"] and not obj["HostConfig"]["PortBindings"]
        assert json.loads(command("docker","network","inspect",name))[0]["Internal"]
        code,status=http(9081,"/v1/runtime/status",key=runtime_token[0])
        assert code==200 and str(status["runtimeGeneration"])==permit["generation"] and status["state"]=="ready"
        assert command("docker","exec",runtime,"sha256sum","/usr/local/bin/cli-proxy-api").split()[0]=="e48c85c75a19adae186cbee590f6e6143fd8085417d8066b545e8533877175f7"
        # No other HTTP/file/reload writer is launched. The original S1 writer
        # is spent; the journal prevents reinstallation. Manager is sole writer.
        source=command("docker","exec","--user","10001:10001",runtime,"sha256sum","/runtime/gateway/config.yaml").split()[0]
        now=int(time.time()*1000)
        value=dict(operationId=operation,step=step,runId=permit["runId"],generation=permit["generation"],sourceSHA256=source,issuedAtMs=now,expiresAtMs=min(now+30000,permit["expiresAtMs"]))
        command("docker","run","--rm","-i","--network","none","--entrypoint","sh","-v",volumes[3]+":/permit",images["manager"],"-ec","umask 077; cat > /permit/key-operation.json; chmod 600 /permit/key-operation.json",data=json.dumps(value))
        record(scenario="dedicated-step-grant",operation=operation,step=step,generation=permit["generation"],sourceSHA256=source,allSourceAndNamespaceConsumersOwned=True)

    def restart(operation):
        command("docker","restart",name+"-manager")
        for _ in range(40):
            try:
                code,r=call("api-keys/operations/"+operation)
                if code==200:break
            except RuntimeError:pass
            time.sleep(.2)
        assert code==200 and r["operation"]["id"]==operation and r["nextStep"]=="revoke"
        assert call("api-keys/operations/"+operation+"/continue","POST",{})[0]==409
        assert call("api-keys/"+old_id+"/revocation","POST",dict(body,requestId=secrets.token_hex(16)))[0]==409

    # Runtime credential read only through the task's existing secret volume.
    runtime_token=[command("docker","exec",runtime,"cat","/run/cpamp/runtime-secret/token")]
    code,created=call("api-keys","POST",{"requestId":secrets.token_hex(16)})
    assert code==201
    new,new_id=created["createdSecret"],created["key"]["id"]
    operation=secrets.token_hex(16)
    body={"requestId":operation,"keeperId":new_id,"mode":"replace"}
    for credential in (None,"wrong"):
        assert call("api-keys/"+old_id+"/revocation","POST",body,credential)[0]==401
    assert call("api-keys/"+old_id+"/revocation","POST",dict(body,mode="revoke"))[0]==409
    assert call("api-keys/"+"f"*32+"/revocation","POST",body)[0] in (404,409)
    record(scenario="last-key-staged-only-keeper-and-unauthorized-refused",noClientKeyWrite=True)
    if os.environ.get("CPAMP_PROOF_KU_BROWSER") == "1":
        from fresh_ku_browser import exercise as browser_exercise
        operation=browser_exercise(command,call,grant,restart,probe,admin,old_id,new_id,out)
        assert operation
        resource="api-keys/operations/"+operation
        code,r=call(resource)
        assert code==200 and r["operation"]["outcome"]=="committed"
        record(scenario="frontend-manager-cpa-ingress-browser",originalOperation=operation)
    else:
        operation=complete_api(call,check,grant,restart,http,old_id,new_id,old,new,body,permit)
    check("replacement-ingress-success",new,200,1)
    for label,key in (("revoked-old",old),("wrong-key","wrong"),("absent-key",None)):
        check(label,key)
    assert call("api-keys/"+old_id)[1]["lifecycle"]=="superseded"
    assert call("api-keys/"+new_id)[1]["lifecycle"]=="active"
    assert call("api-keys/"+new_id+"/revocation","POST",{"requestId":secrets.token_hex(16),"keeperId":old_id,"mode":"revoke"})[0] in (409,503)
    assert http(8317,"/v8/management/config/access/api-keys",key=permit["managementKey"])[1]==[new]
    record(scenario="canonical-history-and-last-key-retained",oldLifecycle="superseded",keeperLifecycle="active",originalOperation=operation,managerRestartRecovered=True)
    evidence["keyOperation"]={"id":operation,"targetId":old_id,"keeperId":new_id,"outcome":"committed","generation":permit["generation"]}


def complete_api(call,check,grant,restart,http,old_id,new_id,old,new,body,permit):
    operation=body["requestId"]
    code,r=call("api-keys/"+old_id+"/revocation","POST",body)
    assert code==200 and r["nextStep"]=="add",("prepare",code,r)
    resource="api-keys/operations/"+operation
    assert call(resource+"/continue","POST",{})[0]==409
    check("prepared-operation-denies-old",old)
    grant(operation,"add")
    code,r=call(resource+"/continue","POST",{})
    # CPA reload is asynchronous; reconcile only, never repeat an attempted PUT.
    for _ in range(12):
        if code==200 and r["nextStep"]=="revoke":break
        time.sleep(.2);code,r=call(resource)
    assert code==200 and r["nextStep"]=="revoke",("add",code,r)
    assert http(8317,"/v8/management/config/access/api-keys",key=permit["managementKey"])[1]==[old,new]
    check("two-key-transition-denies-old",old);check("two-key-transition-denies-replacement",new)
    assert call(resource+"/continue","POST",{})[0]==409
    # Restart Manager, retain original operation, and refuse the old step grant.
    restart(operation)
    grant(operation,"revoke")
    code,r=call(resource+"/continue","POST",{})
    for _ in range(12):
        if code==200 and r["operation"]["outcome"]=="committed":break
        time.sleep(.2);code,r=call(resource)
    assert code==200 and r["operation"]["outcome"]=="committed",("revoke",code,r)
    return operation
