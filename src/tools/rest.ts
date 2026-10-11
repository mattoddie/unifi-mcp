import type { UnifiClient } from "../unifi/client.js";

/**
 * CRUD helpers for the two configuration API styles:
 *  - classic:  /api/s/<site>/rest/<collection>[/<id>]       (responses wrapped in {meta, data})
 *  - v2:       /v2/api/site/<site>/<collection>[/<id>]      (plain JSON)
 *
 * Updates always fetch the current object and PUT the merged result back, since
 * several endpoints reject partial objects.
 */

type Obj = Record<string, any>;

const enc = encodeURIComponent;

export async function restList(client: UnifiClient, site: string, collection: string): Promise<Obj[]> {
  return client.siteGet<Obj[]>(site, `rest/${collection}`);
}

export async function restGet(client: UnifiClient, site: string, collection: string, id: string): Promise<Obj> {
  const [obj] = await client.siteGet<Obj[]>(site, `rest/${collection}/${enc(id)}`);
  if (!obj) throw new Error(`No ${collection} object with id ${id} on site ${site}`);
  return obj;
}

export async function restCreate(client: UnifiClient, site: string, collection: string, body: Obj): Promise<Obj> {
  const res = await client.sitePost<Obj[]>(site, `rest/${collection}`, body);
  return (Array.isArray(res) ? res[0] : res) ?? body;
}

export async function restUpdate(client: UnifiClient, site: string, collection: string, id: string, patch: Obj): Promise<Obj> {
  const current = await restGet(client, site, collection, id);
  const merged = { ...current, ...patch };
  const res = await client.sitePut<Obj[]>(site, `rest/${collection}/${enc(id)}`, merged);
  return (Array.isArray(res) ? res[0] : res) ?? merged;
}

export async function restDelete(client: UnifiClient, site: string, collection: string, id: string): Promise<void> {
  await client.request("DELETE", `/api/s/${enc(site)}/rest/${collection}/${enc(id)}`);
}

/** Create when `id` is undefined, otherwise update. */
export function restSave(client: UnifiClient, site: string, collection: string, id: string | undefined, body: Obj): Promise<Obj> {
  return id ? restUpdate(client, site, collection, id, body) : restCreate(client, site, collection, body);
}

// ---------------------------------------------------------------- v2

export async function v2List(client: UnifiClient, site: string, collection: string): Promise<Obj[]> {
  const res = await client.v2<any>("GET", site, collection);
  return Array.isArray(res) ? res : (res?.data ?? []);
}

export async function v2Get(client: UnifiClient, site: string, collection: string, id: string): Promise<Obj> {
  const all = await v2List(client, site, collection);
  const obj = all.find((o) => o._id === id || o.id === id);
  if (!obj) throw new Error(`No ${collection} object with id ${id} on site ${site}`);
  return obj;
}

export async function v2Create(client: UnifiClient, site: string, collection: string, body: Obj): Promise<Obj> {
  return (await client.v2<Obj>("POST", site, collection, body)) ?? body;
}

export async function v2Update(client: UnifiClient, site: string, collection: string, id: string, patch: Obj): Promise<Obj> {
  const current = await v2Get(client, site, collection, id);
  const merged = { ...current, ...patch };
  return (await client.v2<Obj>("PUT", site, `${collection}/${enc(id)}`, merged)) ?? merged;
}

export async function v2Delete(client: UnifiClient, site: string, collection: string, id: string): Promise<void> {
  await client.v2("DELETE", site, `${collection}/${enc(id)}`);
}

export function v2Save(client: UnifiClient, site: string, collection: string, id: string | undefined, body: Obj): Promise<Obj> {
  return id ? v2Update(client, site, collection, id, body) : v2Create(client, site, collection, body);
}

// ---------------------------------------------------------------- site settings

/** Site settings live in rest/setting as one object per `key` (mgmt, ips, dpi, country, ...). */
export async function getSetting(client: UnifiClient, site: string, key: string): Promise<Obj> {
  const all = await client.siteGet<Obj[]>(site, "rest/setting");
  const obj = all.find((s) => s.key === key);
  if (!obj) throw new Error(`Site setting "${key}" not found; available: ${all.map((s) => s.key).join(", ")}`);
  return obj;
}

export async function updateSetting(client: UnifiClient, site: string, key: string, patch: Obj): Promise<Obj> {
  const current = await getSetting(client, site, key);
  const merged = { ...current, ...patch };
  const res = await client.sitePut<Obj[]>(site, `rest/setting/${enc(key)}/${enc(current._id)}`, merged);
  return (Array.isArray(res) ? res[0] : res) ?? merged;
}
