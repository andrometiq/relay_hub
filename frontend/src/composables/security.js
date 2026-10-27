import { safeServerError } from "../services/hubApi";
export const prefix=location.pathname.split('/auth/')[0]
export async function securityRequest(view,values,signal){
 const options={signal,headers:{Accept:'application/json'}}
 if(values){options.method='POST';options.body=new URLSearchParams();for(const [key,value]of Object.entries(values)){if(Array.isArray(value))value.forEach(v=>options.body.append(key,v));else if(value!=null)options.body.set(key,value)}}
 const r=await fetch(`${prefix}/auth/${view}`,options);const text=await r.text();let data;try{data=JSON.parse(text)}catch{throw new Error(safeServerError(text, 'Request failed. Please sign in again.'))}
 if(!r.ok||data.Error)throw new Error(safeServerError(data.Error || '', 'Request failed'))
 if(data.redirect){location.assign(data.redirect);return data}
 if(view!=='login'&&!view.startsWith('options?')&&!data.User&&!values){location.assign(`${prefix}/auth/login`);throw new Error('Please sign in')}
 return data
}

export const searchSecurityOptions = kind => async (query, signal) => securityRequest('options?'+new URLSearchParams({kind,query}),undefined,signal)
