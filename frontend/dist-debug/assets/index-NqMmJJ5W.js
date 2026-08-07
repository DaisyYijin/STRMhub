(function polyfill() {
  const relList = document.createElement("link").relList;
  if (relList && relList.supports && relList.supports("modulepreload")) {
    return;
  }
  for (const link of document.querySelectorAll('link[rel="modulepreload"]')) {
    processPreload(link);
  }
  new MutationObserver((mutations) => {
    for (const mutation of mutations) {
      if (mutation.type !== "childList") {
        continue;
      }
      for (const node of mutation.addedNodes) {
        if (node.tagName === "LINK" && node.rel === "modulepreload")
          processPreload(node);
      }
    }
  }).observe(document, { childList: true, subtree: true });
  function getFetchOpts(link) {
    const fetchOpts = {};
    if (link.integrity) fetchOpts.integrity = link.integrity;
    if (link.referrerPolicy) fetchOpts.referrerPolicy = link.referrerPolicy;
    if (link.crossOrigin === "use-credentials")
      fetchOpts.credentials = "include";
    else if (link.crossOrigin === "anonymous") fetchOpts.credentials = "omit";
    else fetchOpts.credentials = "same-origin";
    return fetchOpts;
  }
  function processPreload(link) {
    if (link.ep)
      return;
    link.ep = true;
    const fetchOpts = getFetchOpts(link);
    fetch(link.href, fetchOpts);
  }
})();
/**
* @vue/shared v3.5.41
* (c) 2018-present Yuxi (Evan) You and Vue contributors
* @license MIT
**/
// @__NO_SIDE_EFFECTS__
function makeMap(str) {
  const map = /* @__PURE__ */ Object.create(null);
  for (const key of str.split(",")) map[key] = 1;
  return (val) => val in map;
}
const EMPTY_OBJ = {};
const EMPTY_ARR = [];
const NOOP = () => {
};
const NO = () => false;
const isOn = (key) => key.charCodeAt(0) === 111 && key.charCodeAt(1) === 110 && // uppercase letter
(key.charCodeAt(2) > 122 || key.charCodeAt(2) < 97);
const isModelListener = (key) => key.startsWith("onUpdate:");
const extend = Object.assign;
const remove = (arr, el) => {
  const i = arr.indexOf(el);
  if (i > -1) {
    arr.splice(i, 1);
  }
};
const hasOwnProperty$1 = Object.prototype.hasOwnProperty;
const hasOwn = (val, key) => hasOwnProperty$1.call(val, key);
const isArray = Array.isArray;
const isMap = (val) => toTypeString(val) === "[object Map]";
const isSet = (val) => toTypeString(val) === "[object Set]";
const isDate = (val) => toTypeString(val) === "[object Date]";
const isFunction = (val) => typeof val === "function";
const isString = (val) => typeof val === "string";
const isSymbol = (val) => typeof val === "symbol";
const isObject = (val) => val !== null && typeof val === "object";
const isPromise = (val) => {
  return (isObject(val) || isFunction(val)) && isFunction(val.then) && isFunction(val.catch);
};
const objectToString = Object.prototype.toString;
const toTypeString = (value) => objectToString.call(value);
const toRawType = (value) => {
  return toTypeString(value).slice(8, -1);
};
const isPlainObject = (val) => toTypeString(val) === "[object Object]";
const isIntegerKey = (key) => isString(key) && key !== "NaN" && key[0] !== "-" && "" + parseInt(key, 10) === key;
const isReservedProp = /* @__PURE__ */ makeMap(
  // the leading comma is intentional so empty string "" is also included
  ",key,ref,ref_for,ref_key,onVnodeBeforeMount,onVnodeMounted,onVnodeBeforeUpdate,onVnodeUpdated,onVnodeBeforeUnmount,onVnodeUnmounted"
);
const cacheStringFunction = (fn) => {
  const cache = /* @__PURE__ */ Object.create(null);
  return ((str) => {
    const hit = cache[str];
    return hit || (cache[str] = fn(str));
  });
};
const camelizeRE = /-\w/g;
const camelize = cacheStringFunction(
  (str) => {
    return str.replace(camelizeRE, (c) => c.slice(1).toUpperCase());
  }
);
const hyphenateRE = /\B([A-Z])/g;
const hyphenate = cacheStringFunction(
  (str) => str.replace(hyphenateRE, "-$1").toLowerCase()
);
const capitalize = cacheStringFunction((str) => {
  return str.charAt(0).toUpperCase() + str.slice(1);
});
const toHandlerKey = cacheStringFunction(
  (str) => {
    const s = str ? `on${capitalize(str)}` : ``;
    return s;
  }
);
const hasChanged = (value, oldValue) => !Object.is(value, oldValue);
const invokeArrayFns = (fns, ...arg) => {
  for (let i = 0; i < fns.length; i++) {
    fns[i](...arg);
  }
};
const def = (obj, key, value, writable = false) => {
  Object.defineProperty(obj, key, {
    configurable: true,
    enumerable: false,
    writable,
    value
  });
};
const looseToNumber = (val) => {
  const n = parseFloat(val);
  return isNaN(n) ? val : n;
};
let _globalThis;
const getGlobalThis = () => {
  return _globalThis || (_globalThis = typeof globalThis !== "undefined" ? globalThis : typeof self !== "undefined" ? self : typeof window !== "undefined" ? window : typeof global !== "undefined" ? global : {});
};
function normalizeStyle(value) {
  if (isArray(value)) {
    const res = {};
    for (let i = 0; i < value.length; i++) {
      const item = value[i];
      const normalized = isString(item) ? parseStringStyle(item) : normalizeStyle(item);
      if (normalized) {
        for (const key in normalized) {
          res[key] = normalized[key];
        }
      }
    }
    return res;
  } else if (isString(value) || isObject(value)) {
    return value;
  }
}
const listDelimiterRE = /;(?![^(]*\))/g;
const propertyDelimiterRE = /:([^]+)/;
const styleCommentRE = /\/\*[^]*?\*\//g;
function parseStringStyle(cssText) {
  const ret = {};
  cssText.replace(styleCommentRE, "").split(listDelimiterRE).forEach((item) => {
    if (item) {
      const tmp = item.split(propertyDelimiterRE);
      tmp.length > 1 && (ret[tmp[0].trim()] = tmp[1].trim());
    }
  });
  return ret;
}
function normalizeClass(value) {
  let res = "";
  if (isString(value)) {
    res = value;
  } else if (isArray(value)) {
    for (let i = 0; i < value.length; i++) {
      const normalized = normalizeClass(value[i]);
      if (normalized) {
        res += normalized + " ";
      }
    }
  } else if (isObject(value)) {
    for (const name in value) {
      if (value[name]) {
        res += name + " ";
      }
    }
  }
  return res.trim();
}
const specialBooleanAttrs = `itemscope,allowfullscreen,formnovalidate,ismap,nomodule,novalidate,readonly`;
const isSpecialBooleanAttr = /* @__PURE__ */ makeMap(specialBooleanAttrs);
function includeBooleanAttr(value) {
  return !!value || value === "";
}
function looseCompareArrays(a, b) {
  if (a.length !== b.length) return false;
  let equal = true;
  for (let i = 0; equal && i < a.length; i++) {
    equal = looseEqual(a[i], b[i]);
  }
  return equal;
}
function looseEqual(a, b) {
  if (a === b) return true;
  let aValidType = isDate(a);
  let bValidType = isDate(b);
  if (aValidType || bValidType) {
    return aValidType && bValidType ? a.getTime() === b.getTime() : false;
  }
  aValidType = isSymbol(a);
  bValidType = isSymbol(b);
  if (aValidType || bValidType) {
    return a === b;
  }
  aValidType = isArray(a);
  bValidType = isArray(b);
  if (aValidType || bValidType) {
    return aValidType && bValidType ? looseCompareArrays(a, b) : false;
  }
  aValidType = isObject(a);
  bValidType = isObject(b);
  if (aValidType || bValidType) {
    if (!aValidType || !bValidType) {
      return false;
    }
    const aKeysCount = Object.keys(a).length;
    const bKeysCount = Object.keys(b).length;
    if (aKeysCount !== bKeysCount) {
      return false;
    }
    for (const key in a) {
      const aHasKey = a.hasOwnProperty(key);
      const bHasKey = b.hasOwnProperty(key);
      if (aHasKey && !bHasKey || !aHasKey && bHasKey || !looseEqual(a[key], b[key])) {
        return false;
      }
    }
  }
  return String(a) === String(b);
}
function looseIndexOf(arr, val) {
  return arr.findIndex((item) => looseEqual(item, val));
}
const isRef$1 = (val) => {
  return !!(val && val["__v_isRef"] === true);
};
const toDisplayString = (val) => {
  return isString(val) ? val : val == null ? "" : isArray(val) || isObject(val) && (val.toString === objectToString || !isFunction(val.toString)) ? isRef$1(val) ? toDisplayString(val.value) : JSON.stringify(val, replacer, 2) : String(val);
};
const replacer = (_key, val) => {
  if (isRef$1(val)) {
    return replacer(_key, val.value);
  } else if (isMap(val)) {
    return {
      [`Map(${val.size})`]: [...val.entries()].reduce(
        (entries, [key, val2], i) => {
          entries[stringifySymbol(key, i) + " =>"] = val2;
          return entries;
        },
        {}
      )
    };
  } else if (isSet(val)) {
    return {
      [`Set(${val.size})`]: [...val.values()].map((v) => stringifySymbol(v))
    };
  } else if (isSymbol(val)) {
    return stringifySymbol(val);
  } else if (isObject(val) && !isArray(val) && !isPlainObject(val)) {
    return String(val);
  }
  return val;
};
const stringifySymbol = (v, i = "") => {
  var _a;
  return (
    // Symbol.description in es2019+ so we need to cast here to pass
    // the lib: es2016 check
    isSymbol(v) ? `Symbol(${(_a = v.description) != null ? _a : i})` : v
  );
};
/**
* @vue/reactivity v3.5.41
* (c) 2018-present Yuxi (Evan) You and Vue contributors
* @license MIT
**/
let activeEffectScope;
class EffectScope {
  // TODO isolatedDeclarations "__v_skip"
  constructor(detached = false) {
    this.detached = detached;
    this._active = true;
    this._on = 0;
    this.effects = [];
    this.cleanups = [];
    this._isPaused = false;
    this._warnOnRun = true;
    this.__v_skip = true;
    if (!detached && activeEffectScope) {
      if (activeEffectScope.active) {
        this.parent = activeEffectScope;
        this.index = (activeEffectScope.scopes || (activeEffectScope.scopes = [])).push(
          this
        ) - 1;
      } else {
        this._active = false;
        this._warnOnRun = false;
      }
    }
  }
  get active() {
    return this._active;
  }
  pause() {
    if (this._active) {
      this._isPaused = true;
      let i, l;
      if (this.scopes) {
        const scopes = this.scopes.slice();
        for (i = 0, l = scopes.length; i < l; i++) {
          scopes[i].pause();
        }
      }
      for (i = 0, l = this.effects.length; i < l; i++) {
        this.effects[i].pause();
      }
    }
  }
  /**
   * Resumes the effect scope, including all child scopes and effects.
   */
  resume() {
    if (this._active) {
      if (this._isPaused) {
        this._isPaused = false;
        let i, l;
        if (this.scopes) {
          const scopes = this.scopes.slice();
          for (i = 0, l = scopes.length; i < l; i++) {
            scopes[i].resume();
          }
        }
        const effects = this.effects.slice();
        for (i = 0, l = effects.length; i < l; i++) {
          effects[i].resume();
        }
      }
    }
  }
  run(fn) {
    if (this._active) {
      const currentEffectScope = activeEffectScope;
      try {
        activeEffectScope = this;
        return fn();
      } finally {
        activeEffectScope = currentEffectScope;
      }
    }
  }
  /**
   * This should only be called on non-detached scopes
   * @internal
   */
  on() {
    if (++this._on === 1) {
      this.prevScope = activeEffectScope;
      activeEffectScope = this;
    }
  }
  /**
   * This should only be called on non-detached scopes
   * @internal
   */
  off() {
    if (this._on > 0 && --this._on === 0) {
      if (activeEffectScope === this) {
        activeEffectScope = this.prevScope;
      } else {
        let current = activeEffectScope;
        while (current) {
          if (current.prevScope === this) {
            current.prevScope = this.prevScope;
            break;
          }
          current = current.prevScope;
        }
      }
      this.prevScope = void 0;
    }
  }
  stop(fromParent) {
    if (this._active) {
      this._active = false;
      let i, l;
      for (i = 0, l = this.effects.length; i < l; i++) {
        this.effects[i].stop();
      }
      this.effects.length = 0;
      for (i = 0, l = this.cleanups.length; i < l; i++) {
        this.cleanups[i]();
      }
      this.cleanups.length = 0;
      if (this.scopes) {
        const scopes = this.scopes.slice();
        for (i = 0, l = scopes.length; i < l; i++) {
          scopes[i].stop(true);
        }
        this.scopes.length = 0;
      }
      if (!this.detached && this.parent && !fromParent) {
        const last = this.parent.scopes.pop();
        if (last && last !== this) {
          this.parent.scopes[this.index] = last;
          last.index = this.index;
        }
      }
      this.parent = void 0;
    }
  }
}
function getCurrentScope() {
  return activeEffectScope;
}
let activeSub;
const pausedQueueEffects = /* @__PURE__ */ new WeakSet();
class ReactiveEffect {
  constructor(fn) {
    this.fn = fn;
    this.deps = void 0;
    this.depsTail = void 0;
    this.flags = 1 | 4;
    this.next = void 0;
    this.cleanup = void 0;
    this.scheduler = void 0;
    if (activeEffectScope) {
      if (activeEffectScope.active) {
        activeEffectScope.effects.push(this);
      } else {
        this.flags &= -2;
      }
    }
  }
  pause() {
    this.flags |= 64;
  }
  resume() {
    if (this.flags & 64) {
      this.flags &= -65;
      if (pausedQueueEffects.has(this)) {
        pausedQueueEffects.delete(this);
        this.trigger();
      }
    }
  }
  /**
   * @internal
   */
  notify() {
    if (this.flags & 2 && !(this.flags & 32)) {
      return;
    }
    if (!(this.flags & 8)) {
      batch(this);
    }
  }
  run() {
    if (!(this.flags & 1)) {
      return this.fn();
    }
    this.flags |= 2;
    cleanupEffect(this);
    prepareDeps(this);
    const prevEffect = activeSub;
    const prevShouldTrack = shouldTrack;
    activeSub = this;
    shouldTrack = true;
    try {
      return this.fn();
    } finally {
      cleanupDeps(this);
      activeSub = prevEffect;
      shouldTrack = prevShouldTrack;
      this.flags &= -3;
    }
  }
  stop() {
    if (this.flags & 1) {
      for (let link = this.deps; link; link = link.nextDep) {
        removeSub(link);
      }
      this.deps = this.depsTail = void 0;
      cleanupEffect(this);
      this.onStop && this.onStop();
      this.flags &= -2;
    }
  }
  trigger() {
    if (this.flags & 64) {
      pausedQueueEffects.add(this);
    } else if (this.scheduler) {
      this.scheduler();
    } else {
      this.runIfDirty();
    }
  }
  /**
   * @internal
   */
  runIfDirty() {
    if (isDirty(this)) {
      this.run();
    }
  }
  get dirty() {
    return isDirty(this);
  }
}
let batchDepth = 0;
let batchedSub;
let batchedComputed;
function batch(sub, isComputed = false) {
  sub.flags |= 8;
  if (isComputed) {
    sub.next = batchedComputed;
    batchedComputed = sub;
    return;
  }
  sub.next = batchedSub;
  batchedSub = sub;
}
function startBatch() {
  batchDepth++;
}
function endBatch() {
  if (--batchDepth > 0) {
    return;
  }
  if (batchedComputed) {
    let e = batchedComputed;
    batchedComputed = void 0;
    while (e) {
      const next = e.next;
      e.next = void 0;
      e.flags &= -9;
      e = next;
    }
  }
  let error;
  while (batchedSub) {
    let e = batchedSub;
    batchedSub = void 0;
    while (e) {
      const next = e.next;
      e.next = void 0;
      e.flags &= -9;
      if (e.flags & 1) {
        try {
          ;
          e.trigger();
        } catch (err) {
          if (!error) error = err;
        }
      }
      e = next;
    }
  }
  if (error) throw error;
}
function prepareDeps(sub) {
  for (let link = sub.deps; link; link = link.nextDep) {
    link.version = -1;
    link.prevActiveLink = link.dep.activeLink;
    link.dep.activeLink = link;
  }
}
function cleanupDeps(sub) {
  let head;
  let tail = sub.depsTail;
  let link = tail;
  while (link) {
    const prev = link.prevDep;
    if (link.version === -1) {
      if (link === tail) tail = prev;
      removeSub(link);
      removeDep(link);
    } else {
      head = link;
    }
    link.dep.activeLink = link.prevActiveLink;
    link.prevActiveLink = void 0;
    link = prev;
  }
  sub.deps = head;
  sub.depsTail = tail;
}
function isDirty(sub) {
  for (let link = sub.deps; link; link = link.nextDep) {
    if (link.dep.version !== link.version || link.dep.computed && (refreshComputed(link.dep.computed) || link.dep.version !== link.version)) {
      return true;
    }
  }
  if (sub._dirty) {
    return true;
  }
  return false;
}
function refreshComputed(computed2) {
  if (computed2.flags & 4 && !(computed2.flags & 16)) {
    return;
  }
  computed2.flags &= -17;
  if (computed2.globalVersion === globalVersion) {
    return;
  }
  computed2.globalVersion = globalVersion;
  if (!computed2.isSSR && computed2.flags & 128 && (!computed2.deps && !computed2._dirty || !isDirty(computed2))) {
    return;
  }
  computed2.flags |= 2;
  const dep = computed2.dep;
  const prevSub = activeSub;
  const prevShouldTrack = shouldTrack;
  activeSub = computed2;
  shouldTrack = true;
  try {
    prepareDeps(computed2);
    const value = computed2.fn(computed2._value);
    if (dep.version === 0 || hasChanged(value, computed2._value)) {
      computed2.flags |= 128;
      computed2._value = value;
      dep.version++;
    }
  } catch (err) {
    dep.version++;
    throw err;
  } finally {
    activeSub = prevSub;
    shouldTrack = prevShouldTrack;
    cleanupDeps(computed2);
    computed2.flags &= -3;
  }
}
function removeSub(link, soft = false) {
  const { dep, prevSub, nextSub } = link;
  if (prevSub) {
    prevSub.nextSub = nextSub;
    link.prevSub = void 0;
  }
  if (nextSub) {
    nextSub.prevSub = prevSub;
    link.nextSub = void 0;
  }
  if (dep.subs === link) {
    dep.subs = prevSub;
    if (!prevSub && dep.computed) {
      dep.computed.flags &= -5;
      for (let l = dep.computed.deps; l; l = l.nextDep) {
        removeSub(l, true);
      }
    }
  }
  if (!soft && !--dep.sc && dep.map) {
    dep.map.delete(dep.key);
  }
}
function removeDep(link) {
  const { prevDep, nextDep } = link;
  if (prevDep) {
    prevDep.nextDep = nextDep;
    link.prevDep = void 0;
  }
  if (nextDep) {
    nextDep.prevDep = prevDep;
    link.nextDep = void 0;
  }
}
let shouldTrack = true;
const trackStack = [];
function pauseTracking() {
  trackStack.push(shouldTrack);
  shouldTrack = false;
}
function resetTracking() {
  const last = trackStack.pop();
  shouldTrack = last === void 0 ? true : last;
}
function cleanupEffect(e) {
  const { cleanup } = e;
  e.cleanup = void 0;
  if (cleanup) {
    const prevSub = activeSub;
    activeSub = void 0;
    try {
      cleanup();
    } finally {
      activeSub = prevSub;
    }
  }
}
let globalVersion = 0;
class Link {
  constructor(sub, dep) {
    this.sub = sub;
    this.dep = dep;
    this.version = dep.version;
    this.nextDep = this.prevDep = this.nextSub = this.prevSub = this.prevActiveLink = void 0;
  }
}
class Dep {
  // TODO isolatedDeclarations "__v_skip"
  constructor(computed2) {
    this.computed = computed2;
    this.version = 0;
    this.activeLink = void 0;
    this.subs = void 0;
    this.map = void 0;
    this.key = void 0;
    this.sc = 0;
    this.__v_skip = true;
  }
  track(debugInfo) {
    if (!activeSub || !shouldTrack || activeSub === this.computed) {
      return;
    }
    let link = this.activeLink;
    if (link === void 0 || link.sub !== activeSub) {
      link = this.activeLink = new Link(activeSub, this);
      if (!activeSub.deps) {
        activeSub.deps = activeSub.depsTail = link;
      } else {
        link.prevDep = activeSub.depsTail;
        activeSub.depsTail.nextDep = link;
        activeSub.depsTail = link;
      }
      addSub(link);
    } else if (link.version === -1) {
      link.version = this.version;
      if (link.nextDep) {
        const next = link.nextDep;
        next.prevDep = link.prevDep;
        if (link.prevDep) {
          link.prevDep.nextDep = next;
        }
        link.prevDep = activeSub.depsTail;
        link.nextDep = void 0;
        activeSub.depsTail.nextDep = link;
        activeSub.depsTail = link;
        if (activeSub.deps === link) {
          activeSub.deps = next;
        }
      }
    }
    return link;
  }
  trigger(debugInfo) {
    this.version++;
    globalVersion++;
    this.notify(debugInfo);
  }
  notify(debugInfo) {
    startBatch();
    try {
      if (false) ;
      for (let link = this.subs; link; link = link.prevSub) {
        if (link.sub.notify()) {
          ;
          link.sub.dep.notify();
        }
      }
    } finally {
      endBatch();
    }
  }
}
function addSub(link) {
  link.dep.sc++;
  if (link.sub.flags & 4) {
    const computed2 = link.dep.computed;
    if (computed2 && !link.dep.subs) {
      computed2.flags |= 4 | 16;
      for (let l = computed2.deps; l; l = l.nextDep) {
        addSub(l);
      }
    }
    const currentTail = link.dep.subs;
    if (currentTail !== link) {
      link.prevSub = currentTail;
      if (currentTail) currentTail.nextSub = link;
    }
    link.dep.subs = link;
  }
}
const targetMap = /* @__PURE__ */ new WeakMap();
const ITERATE_KEY = /* @__PURE__ */ Symbol(
  ""
);
const MAP_KEY_ITERATE_KEY = /* @__PURE__ */ Symbol(
  ""
);
const ARRAY_ITERATE_KEY = /* @__PURE__ */ Symbol(
  ""
);
function track(target, type, key) {
  if (shouldTrack && activeSub) {
    let depsMap = targetMap.get(target);
    if (!depsMap) {
      targetMap.set(target, depsMap = /* @__PURE__ */ new Map());
    }
    let dep = depsMap.get(key);
    if (!dep) {
      depsMap.set(key, dep = new Dep());
      dep.map = depsMap;
      dep.key = key;
    }
    {
      dep.track();
    }
  }
}
function trigger(target, type, key, newValue, oldValue, oldTarget) {
  const depsMap = targetMap.get(target);
  if (!depsMap) {
    globalVersion++;
    return;
  }
  const run = (dep) => {
    if (dep) {
      {
        dep.trigger();
      }
    }
  };
  startBatch();
  if (type === "clear") {
    depsMap.forEach(run);
  } else {
    const targetIsArray = isArray(target);
    const isArrayIndex = targetIsArray && isIntegerKey(key);
    if (targetIsArray && key === "length") {
      const newLength = Number(newValue);
      depsMap.forEach((dep, key2) => {
        if (key2 === "length" || key2 === ARRAY_ITERATE_KEY || !isSymbol(key2) && key2 >= newLength) {
          run(dep);
        }
      });
    } else {
      if (key !== void 0 || depsMap.has(void 0)) {
        run(depsMap.get(key));
      }
      if (isArrayIndex) {
        run(depsMap.get(ARRAY_ITERATE_KEY));
      }
      switch (type) {
        case "add":
          if (!targetIsArray) {
            run(depsMap.get(ITERATE_KEY));
            if (isMap(target)) {
              run(depsMap.get(MAP_KEY_ITERATE_KEY));
            }
          } else if (isArrayIndex) {
            run(depsMap.get("length"));
          }
          break;
        case "delete":
          if (!targetIsArray) {
            run(depsMap.get(ITERATE_KEY));
            if (isMap(target)) {
              run(depsMap.get(MAP_KEY_ITERATE_KEY));
            }
          }
          break;
        case "set":
          if (isMap(target)) {
            run(depsMap.get(ITERATE_KEY));
          }
          break;
      }
    }
  }
  endBatch();
}
function reactiveReadArray(array) {
  const raw = /* @__PURE__ */ toRaw(array);
  if (raw === array) return raw;
  track(raw, "iterate", ARRAY_ITERATE_KEY);
  return /* @__PURE__ */ isShallow(array) ? raw : raw.map(toReactive);
}
function shallowReadArray(arr) {
  track(arr = /* @__PURE__ */ toRaw(arr), "iterate", ARRAY_ITERATE_KEY);
  return arr;
}
function toWrapped(target, item) {
  if (/* @__PURE__ */ isReadonly(target)) {
    return /* @__PURE__ */ isReactive(target) ? toReadonly(toReactive(item)) : toReadonly(item);
  }
  return toReactive(item);
}
const arrayInstrumentations = {
  __proto__: null,
  [Symbol.iterator]() {
    return iterator(this, Symbol.iterator, (item) => toWrapped(this, item));
  },
  concat(...args) {
    return reactiveReadArray(this).concat(
      ...args.map((x) => isArray(x) ? reactiveReadArray(x) : x)
    );
  },
  entries() {
    return iterator(this, "entries", (value) => {
      value[1] = toWrapped(this, value[1]);
      return value;
    });
  },
  every(fn, thisArg) {
    return apply(this, "every", fn, thisArg, void 0, arguments);
  },
  filter(fn, thisArg) {
    return apply(
      this,
      "filter",
      fn,
      thisArg,
      (v) => v.map((item) => toWrapped(this, item)),
      arguments
    );
  },
  find(fn, thisArg) {
    return apply(
      this,
      "find",
      fn,
      thisArg,
      (item) => toWrapped(this, item),
      arguments
    );
  },
  findIndex(fn, thisArg) {
    return apply(this, "findIndex", fn, thisArg, void 0, arguments);
  },
  findLast(fn, thisArg) {
    return apply(
      this,
      "findLast",
      fn,
      thisArg,
      (item) => toWrapped(this, item),
      arguments
    );
  },
  findLastIndex(fn, thisArg) {
    return apply(this, "findLastIndex", fn, thisArg, void 0, arguments);
  },
  // flat, flatMap could benefit from ARRAY_ITERATE but are not straight-forward to implement
  forEach(fn, thisArg) {
    return apply(this, "forEach", fn, thisArg, void 0, arguments);
  },
  includes(...args) {
    return searchProxy(this, "includes", args);
  },
  indexOf(...args) {
    return searchProxy(this, "indexOf", args);
  },
  join(separator) {
    return reactiveReadArray(this).join(separator);
  },
  // keys() iterator only reads `length`, no optimization required
  lastIndexOf(...args) {
    return searchProxy(this, "lastIndexOf", args);
  },
  map(fn, thisArg) {
    return apply(this, "map", fn, thisArg, void 0, arguments);
  },
  pop() {
    return noTracking(this, "pop");
  },
  push(...args) {
    return noTracking(this, "push", args);
  },
  reduce(fn, ...args) {
    return reduce(this, "reduce", fn, args);
  },
  reduceRight(fn, ...args) {
    return reduce(this, "reduceRight", fn, args);
  },
  shift() {
    return noTracking(this, "shift");
  },
  // slice could use ARRAY_ITERATE but also seems to beg for range tracking
  some(fn, thisArg) {
    return apply(this, "some", fn, thisArg, void 0, arguments);
  },
  splice(...args) {
    return noTracking(this, "splice", args);
  },
  toReversed() {
    return reactiveReadArray(this).toReversed();
  },
  toSorted(comparer) {
    return reactiveReadArray(this).toSorted(comparer);
  },
  toSpliced(...args) {
    return reactiveReadArray(this).toSpliced(...args);
  },
  unshift(...args) {
    return noTracking(this, "unshift", args);
  },
  values() {
    return iterator(this, "values", (item) => toWrapped(this, item));
  }
};
function iterator(self2, method, wrapValue) {
  const arr = shallowReadArray(self2);
  const iter = arr[method]();
  if (arr !== self2 && !/* @__PURE__ */ isShallow(self2)) {
    iter._next = iter.next;
    iter.next = () => {
      const result = iter._next();
      if (!result.done) {
        result.value = wrapValue(result.value);
      }
      return result;
    };
  }
  return iter;
}
const arrayProto = Array.prototype;
function apply(self2, method, fn, thisArg, wrappedRetFn, args) {
  const arr = shallowReadArray(self2);
  const needsWrap = arr !== self2 && !/* @__PURE__ */ isShallow(self2);
  const methodFn = arr[method];
  if (methodFn !== arrayProto[method]) {
    const result2 = methodFn.apply(self2, args);
    return needsWrap ? toReactive(result2) : result2;
  }
  let wrappedFn = fn;
  if (arr !== self2) {
    if (needsWrap) {
      wrappedFn = function(item, index) {
        return fn.call(this, toWrapped(self2, item), index, self2);
      };
    } else if (fn.length > 2) {
      wrappedFn = function(item, index) {
        return fn.call(this, item, index, self2);
      };
    }
  }
  const result = methodFn.call(arr, wrappedFn, thisArg);
  return needsWrap && wrappedRetFn ? wrappedRetFn(result) : result;
}
function reduce(self2, method, fn, args) {
  const arr = shallowReadArray(self2);
  const needsWrap = arr !== self2 && !/* @__PURE__ */ isShallow(self2);
  let wrappedFn = fn;
  let wrapInitialAccumulator = false;
  if (arr !== self2) {
    if (needsWrap) {
      wrapInitialAccumulator = args.length === 0;
      wrappedFn = function(acc, item, index) {
        if (wrapInitialAccumulator) {
          wrapInitialAccumulator = false;
          acc = toWrapped(self2, acc);
        }
        return fn.call(this, acc, toWrapped(self2, item), index, self2);
      };
    } else if (fn.length > 3) {
      wrappedFn = function(acc, item, index) {
        return fn.call(this, acc, item, index, self2);
      };
    }
  }
  const result = arr[method](wrappedFn, ...args);
  return wrapInitialAccumulator ? toWrapped(self2, result) : result;
}
function searchProxy(self2, method, args) {
  const arr = /* @__PURE__ */ toRaw(self2);
  track(arr, "iterate", ARRAY_ITERATE_KEY);
  const res = arr[method](...args);
  if ((res === -1 || res === false) && /* @__PURE__ */ isProxy(args[0])) {
    args[0] = /* @__PURE__ */ toRaw(args[0]);
    return arr[method](...args);
  }
  return res;
}
function noTracking(self2, method, args = []) {
  pauseTracking();
  startBatch();
  const res = (/* @__PURE__ */ toRaw(self2))[method].apply(self2, args);
  endBatch();
  resetTracking();
  return res;
}
const isNonTrackableKeys = /* @__PURE__ */ makeMap(`__proto__,__v_isRef,__isVue`);
const builtInSymbols = new Set(
  /* @__PURE__ */ Object.getOwnPropertyNames(Symbol).filter((key) => key !== "arguments" && key !== "caller").map((key) => Symbol[key]).filter(isSymbol)
);
function hasOwnProperty(key) {
  if (!isSymbol(key)) key = String(key);
  const obj = /* @__PURE__ */ toRaw(this);
  track(obj, "has", key);
  return obj.hasOwnProperty(key);
}
class BaseReactiveHandler {
  constructor(_isReadonly = false, _isShallow = false) {
    this._isReadonly = _isReadonly;
    this._isShallow = _isShallow;
  }
  get(target, key, receiver) {
    if (key === "__v_skip") return target["__v_skip"];
    const isReadonly2 = this._isReadonly, isShallow2 = this._isShallow;
    if (key === "__v_isReactive") {
      return !isReadonly2;
    } else if (key === "__v_isReadonly") {
      return isReadonly2;
    } else if (key === "__v_isShallow") {
      return isShallow2;
    } else if (key === "__v_raw") {
      if (receiver === (isReadonly2 ? isShallow2 ? shallowReadonlyMap : readonlyMap : isShallow2 ? shallowReactiveMap : reactiveMap).get(target) || // receiver is not the reactive proxy, but has the same prototype
      // this means the receiver is a user proxy of the reactive proxy
      Object.getPrototypeOf(target) === Object.getPrototypeOf(receiver)) {
        return target;
      }
      return;
    }
    const targetIsArray = isArray(target);
    if (!isReadonly2) {
      let fn;
      if (targetIsArray && (fn = arrayInstrumentations[key])) {
        return fn;
      }
      if (key === "hasOwnProperty") {
        return hasOwnProperty;
      }
    }
    const res = Reflect.get(
      target,
      key,
      // if this is a proxy wrapping a ref, return methods using the raw ref
      // as receiver so that we don't have to call `toRaw` on the ref in all
      // its class methods
      /* @__PURE__ */ isRef(target) ? target : receiver
    );
    if (isSymbol(key) ? builtInSymbols.has(key) : isNonTrackableKeys(key)) {
      return res;
    }
    if (!isReadonly2) {
      track(target, "get", key);
    }
    if (isShallow2) {
      return res;
    }
    if (/* @__PURE__ */ isRef(res)) {
      const value = targetIsArray && isIntegerKey(key) ? res : res.value;
      return isReadonly2 && isObject(value) ? /* @__PURE__ */ readonly(value) : value;
    }
    if (isObject(res)) {
      return isReadonly2 ? /* @__PURE__ */ readonly(res) : /* @__PURE__ */ reactive(res);
    }
    return res;
  }
}
class MutableReactiveHandler extends BaseReactiveHandler {
  constructor(isShallow2 = false) {
    super(false, isShallow2);
  }
  set(target, key, value, receiver) {
    let oldValue = target[key];
    const isArrayWithIntegerKey = isArray(target) && isIntegerKey(key);
    if (!this._isShallow) {
      const isOldValueReadonly = /* @__PURE__ */ isReadonly(oldValue);
      if (!/* @__PURE__ */ isShallow(value) && !/* @__PURE__ */ isReadonly(value)) {
        oldValue = /* @__PURE__ */ toRaw(oldValue);
        value = /* @__PURE__ */ toRaw(value);
      }
      if (!isArrayWithIntegerKey && /* @__PURE__ */ isRef(oldValue) && !/* @__PURE__ */ isRef(value)) {
        if (isOldValueReadonly) {
          return true;
        } else {
          oldValue.value = value;
          return true;
        }
      }
    }
    const hadKey = isArrayWithIntegerKey ? Number(key) < target.length : hasOwn(target, key);
    const result = Reflect.set(
      target,
      key,
      value,
      /* @__PURE__ */ isRef(target) ? target : receiver
    );
    if (target === /* @__PURE__ */ toRaw(receiver) && result) {
      if (!hadKey) {
        trigger(target, "add", key, value);
      } else if (hasChanged(value, oldValue)) {
        trigger(target, "set", key, value);
      }
    }
    return result;
  }
  deleteProperty(target, key) {
    const hadKey = hasOwn(target, key);
    target[key];
    const result = Reflect.deleteProperty(target, key);
    if (result && hadKey) {
      trigger(target, "delete", key, void 0);
    }
    return result;
  }
  has(target, key) {
    const result = Reflect.has(target, key);
    if (!isSymbol(key) || !builtInSymbols.has(key)) {
      track(target, "has", key);
    }
    return result;
  }
  ownKeys(target) {
    track(
      target,
      "iterate",
      isArray(target) ? "length" : ITERATE_KEY
    );
    return Reflect.ownKeys(target);
  }
}
class ReadonlyReactiveHandler extends BaseReactiveHandler {
  constructor(isShallow2 = false) {
    super(true, isShallow2);
  }
  set(target, key) {
    return true;
  }
  deleteProperty(target, key) {
    return true;
  }
}
const mutableHandlers = /* @__PURE__ */ new MutableReactiveHandler();
const readonlyHandlers = /* @__PURE__ */ new ReadonlyReactiveHandler();
const shallowReactiveHandlers = /* @__PURE__ */ new MutableReactiveHandler(true);
const shallowReadonlyHandlers = /* @__PURE__ */ new ReadonlyReactiveHandler(true);
const toShallow = (value) => value;
const getProto = (v) => Reflect.getPrototypeOf(v);
function createIterableMethod(method, isReadonly2, isShallow2) {
  return function(...args) {
    const target = this["__v_raw"];
    const rawTarget = /* @__PURE__ */ toRaw(target);
    const targetIsMap = isMap(rawTarget);
    const isPair = method === "entries" || method === Symbol.iterator && targetIsMap;
    const isKeyOnly = method === "keys" && targetIsMap;
    const innerIterator = target[method](...args);
    const wrap = isShallow2 ? toShallow : isReadonly2 ? toReadonly : toReactive;
    !isReadonly2 && track(
      rawTarget,
      "iterate",
      isKeyOnly ? MAP_KEY_ITERATE_KEY : ITERATE_KEY
    );
    return extend(
      // inheriting all iterator properties
      Object.create(innerIterator),
      {
        // iterator protocol
        next() {
          const { value, done } = innerIterator.next();
          return done ? { value, done } : {
            value: isPair ? [wrap(value[0]), wrap(value[1])] : wrap(value),
            done
          };
        }
      }
    );
  };
}
function createReadonlyMethod(type) {
  return function(...args) {
    return type === "delete" ? false : type === "clear" ? void 0 : this;
  };
}
function createInstrumentations(readonly2, shallow) {
  const instrumentations = {
    get(key) {
      const target = this["__v_raw"];
      const rawTarget = /* @__PURE__ */ toRaw(target);
      const rawKey = /* @__PURE__ */ toRaw(key);
      if (!readonly2) {
        if (hasChanged(key, rawKey)) {
          track(rawTarget, "get", key);
        }
        track(rawTarget, "get", rawKey);
      }
      const { has } = getProto(rawTarget);
      const wrap = shallow ? toShallow : readonly2 ? toReadonly : toReactive;
      if (has.call(rawTarget, key)) {
        return wrap(target.get(key));
      } else if (has.call(rawTarget, rawKey)) {
        return wrap(target.get(rawKey));
      } else if (target !== rawTarget) {
        target.get(key);
      }
    },
    get size() {
      const target = this["__v_raw"];
      !readonly2 && track(/* @__PURE__ */ toRaw(target), "iterate", ITERATE_KEY);
      return target.size;
    },
    has(key) {
      const target = this["__v_raw"];
      const rawTarget = /* @__PURE__ */ toRaw(target);
      const rawKey = /* @__PURE__ */ toRaw(key);
      if (!readonly2) {
        if (hasChanged(key, rawKey)) {
          track(rawTarget, "has", key);
        }
        track(rawTarget, "has", rawKey);
      }
      return key === rawKey ? target.has(key) : target.has(key) || target.has(rawKey);
    },
    forEach(callback, thisArg) {
      const observed = this;
      const target = observed["__v_raw"];
      const rawTarget = /* @__PURE__ */ toRaw(target);
      const wrap = shallow ? toShallow : readonly2 ? toReadonly : toReactive;
      !readonly2 && track(rawTarget, "iterate", ITERATE_KEY);
      return target.forEach((value, key) => {
        return callback.call(thisArg, wrap(value), wrap(key), observed);
      });
    }
  };
  extend(
    instrumentations,
    readonly2 ? {
      add: createReadonlyMethod("add"),
      set: createReadonlyMethod("set"),
      delete: createReadonlyMethod("delete"),
      clear: createReadonlyMethod("clear")
    } : {
      add(value) {
        const target = /* @__PURE__ */ toRaw(this);
        const proto = getProto(target);
        const rawValue = /* @__PURE__ */ toRaw(value);
        const valueToAdd = !shallow && !/* @__PURE__ */ isShallow(value) && !/* @__PURE__ */ isReadonly(value) ? rawValue : value;
        const hadKey = proto.has.call(target, valueToAdd) || hasChanged(value, valueToAdd) && proto.has.call(target, value) || hasChanged(rawValue, valueToAdd) && proto.has.call(target, rawValue);
        if (!hadKey) {
          target.add(valueToAdd);
          trigger(target, "add", valueToAdd, valueToAdd);
        }
        return this;
      },
      set(key, value) {
        if (!shallow && !/* @__PURE__ */ isShallow(value) && !/* @__PURE__ */ isReadonly(value)) {
          value = /* @__PURE__ */ toRaw(value);
        }
        const target = /* @__PURE__ */ toRaw(this);
        const { has, get } = getProto(target);
        let hadKey = has.call(target, key);
        if (!hadKey) {
          key = /* @__PURE__ */ toRaw(key);
          hadKey = has.call(target, key);
        }
        const oldValue = get.call(target, key);
        target.set(key, value);
        if (!hadKey) {
          trigger(target, "add", key, value);
        } else if (hasChanged(value, oldValue)) {
          trigger(target, "set", key, value);
        }
        return this;
      },
      delete(key) {
        const target = /* @__PURE__ */ toRaw(this);
        const { has, get } = getProto(target);
        let hadKey = has.call(target, key);
        if (!hadKey) {
          key = /* @__PURE__ */ toRaw(key);
          hadKey = has.call(target, key);
        }
        get ? get.call(target, key) : void 0;
        const result = target.delete(key);
        if (hadKey) {
          trigger(target, "delete", key, void 0);
        }
        return result;
      },
      clear() {
        const target = /* @__PURE__ */ toRaw(this);
        const hadItems = target.size !== 0;
        const result = target.clear();
        if (hadItems) {
          trigger(
            target,
            "clear",
            void 0,
            void 0
          );
        }
        return result;
      }
    }
  );
  const iteratorMethods = [
    "keys",
    "values",
    "entries",
    Symbol.iterator
  ];
  iteratorMethods.forEach((method) => {
    instrumentations[method] = createIterableMethod(method, readonly2, shallow);
  });
  return instrumentations;
}
function createInstrumentationGetter(isReadonly2, shallow) {
  const instrumentations = createInstrumentations(isReadonly2, shallow);
  return (target, key, receiver) => {
    if (key === "__v_isReactive") {
      return !isReadonly2;
    } else if (key === "__v_isReadonly") {
      return isReadonly2;
    } else if (key === "__v_raw") {
      return target;
    }
    return Reflect.get(
      hasOwn(instrumentations, key) && key in target ? instrumentations : target,
      key,
      receiver
    );
  };
}
const mutableCollectionHandlers = {
  get: /* @__PURE__ */ createInstrumentationGetter(false, false)
};
const shallowCollectionHandlers = {
  get: /* @__PURE__ */ createInstrumentationGetter(false, true)
};
const readonlyCollectionHandlers = {
  get: /* @__PURE__ */ createInstrumentationGetter(true, false)
};
const shallowReadonlyCollectionHandlers = {
  get: /* @__PURE__ */ createInstrumentationGetter(true, true)
};
const reactiveMap = /* @__PURE__ */ new WeakMap();
const shallowReactiveMap = /* @__PURE__ */ new WeakMap();
const readonlyMap = /* @__PURE__ */ new WeakMap();
const shallowReadonlyMap = /* @__PURE__ */ new WeakMap();
function targetTypeMap(rawType) {
  switch (rawType) {
    case "Object":
    case "Array":
      return 1;
    case "Map":
    case "Set":
    case "WeakMap":
    case "WeakSet":
      return 2;
    default:
      return 0;
  }
}
// @__NO_SIDE_EFFECTS__
function reactive(target) {
  if (/* @__PURE__ */ isReadonly(target)) {
    return target;
  }
  return createReactiveObject(
    target,
    false,
    mutableHandlers,
    mutableCollectionHandlers,
    reactiveMap
  );
}
// @__NO_SIDE_EFFECTS__
function shallowReactive(target) {
  return createReactiveObject(
    target,
    false,
    shallowReactiveHandlers,
    shallowCollectionHandlers,
    shallowReactiveMap
  );
}
// @__NO_SIDE_EFFECTS__
function readonly(target) {
  return createReactiveObject(
    target,
    true,
    readonlyHandlers,
    readonlyCollectionHandlers,
    readonlyMap
  );
}
// @__NO_SIDE_EFFECTS__
function shallowReadonly(target) {
  return createReactiveObject(
    target,
    true,
    shallowReadonlyHandlers,
    shallowReadonlyCollectionHandlers,
    shallowReadonlyMap
  );
}
function createReactiveObject(target, isReadonly2, baseHandlers, collectionHandlers, proxyMap) {
  if (!isObject(target)) {
    return target;
  }
  if (target["__v_raw"] && !(isReadonly2 && target["__v_isReactive"])) {
    return target;
  }
  if (target["__v_skip"] || !Object.isExtensible(target)) {
    return target;
  }
  const existingProxy = proxyMap.get(target);
  if (existingProxy) {
    return existingProxy;
  }
  const targetType = targetTypeMap(toRawType(target));
  if (targetType === 0) {
    return target;
  }
  const proxy = new Proxy(
    target,
    targetType === 2 ? collectionHandlers : baseHandlers
  );
  proxyMap.set(target, proxy);
  return proxy;
}
// @__NO_SIDE_EFFECTS__
function isReactive(value) {
  if (/* @__PURE__ */ isReadonly(value)) {
    return /* @__PURE__ */ isReactive(value["__v_raw"]);
  }
  return !!(value && value["__v_isReactive"]);
}
// @__NO_SIDE_EFFECTS__
function isReadonly(value) {
  return !!(value && value["__v_isReadonly"]);
}
// @__NO_SIDE_EFFECTS__
function isShallow(value) {
  return !!(value && value["__v_isShallow"]);
}
// @__NO_SIDE_EFFECTS__
function isProxy(value) {
  return value ? !!value["__v_raw"] : false;
}
// @__NO_SIDE_EFFECTS__
function toRaw(observed) {
  const raw = observed && observed["__v_raw"];
  return raw ? /* @__PURE__ */ toRaw(raw) : observed;
}
function markRaw(value) {
  if (!hasOwn(value, "__v_skip") && Object.isExtensible(value)) {
    def(value, "__v_skip", true);
  }
  return value;
}
const toReactive = (value) => isObject(value) ? /* @__PURE__ */ reactive(value) : value;
const toReadonly = (value) => isObject(value) ? /* @__PURE__ */ readonly(value) : value;
// @__NO_SIDE_EFFECTS__
function isRef(r) {
  return r ? r["__v_isRef"] === true : false;
}
// @__NO_SIDE_EFFECTS__
function ref(value) {
  return createRef(value, false);
}
function createRef(rawValue, shallow) {
  if (/* @__PURE__ */ isRef(rawValue)) {
    return rawValue;
  }
  return new RefImpl(rawValue, shallow);
}
class RefImpl {
  constructor(value, isShallow2) {
    this.dep = new Dep();
    this["__v_isRef"] = true;
    this["__v_isShallow"] = false;
    this._rawValue = isShallow2 ? value : /* @__PURE__ */ toRaw(value);
    this._value = isShallow2 ? value : toReactive(value);
    this["__v_isShallow"] = isShallow2;
  }
  get value() {
    {
      this.dep.track();
    }
    return this._value;
  }
  set value(newValue) {
    const oldValue = this._rawValue;
    const useDirectValue = this["__v_isShallow"] || /* @__PURE__ */ isShallow(newValue) || /* @__PURE__ */ isReadonly(newValue);
    newValue = useDirectValue ? newValue : /* @__PURE__ */ toRaw(newValue);
    if (hasChanged(newValue, oldValue)) {
      this._rawValue = newValue;
      this._value = useDirectValue ? newValue : toReactive(newValue);
      {
        this.dep.trigger();
      }
    }
  }
}
function unref(ref2) {
  return /* @__PURE__ */ isRef(ref2) ? ref2.value : ref2;
}
const shallowUnwrapHandlers = {
  get: (target, key, receiver) => key === "__v_raw" ? target : unref(Reflect.get(target, key, receiver)),
  set: (target, key, value, receiver) => {
    const oldValue = target[key];
    if (/* @__PURE__ */ isRef(oldValue) && !/* @__PURE__ */ isRef(value)) {
      oldValue.value = value;
      return true;
    } else {
      return Reflect.set(target, key, value, receiver);
    }
  }
};
function proxyRefs(objectWithRefs) {
  return /* @__PURE__ */ isReactive(objectWithRefs) ? objectWithRefs : new Proxy(objectWithRefs, shallowUnwrapHandlers);
}
class ComputedRefImpl {
  constructor(fn, setter, isSSR) {
    this.fn = fn;
    this.setter = setter;
    this._value = void 0;
    this.dep = new Dep(this);
    this.__v_isRef = true;
    this.deps = void 0;
    this.depsTail = void 0;
    this.flags = 16;
    this.globalVersion = globalVersion - 1;
    this.next = void 0;
    this.effect = this;
    this["__v_isReadonly"] = !setter;
    this.isSSR = isSSR;
  }
  /**
   * @internal
   */
  notify() {
    this.flags |= 16;
    if (!(this.flags & 8) && // avoid infinite self recursion
    activeSub !== this) {
      batch(this, true);
      return true;
    }
  }
  get value() {
    const link = this.dep.track();
    refreshComputed(this);
    if (link) {
      link.version = this.dep.version;
    }
    return this._value;
  }
  set value(newValue) {
    if (this.setter) {
      this.setter(newValue);
    }
  }
}
// @__NO_SIDE_EFFECTS__
function computed$1(getterOrOptions, debugOptions, isSSR = false) {
  let getter;
  let setter;
  if (isFunction(getterOrOptions)) {
    getter = getterOrOptions;
  } else {
    getter = getterOrOptions.get;
    setter = getterOrOptions.set;
  }
  const cRef = new ComputedRefImpl(getter, setter, isSSR);
  return cRef;
}
const INITIAL_WATCHER_VALUE = {};
const cleanupMap = /* @__PURE__ */ new WeakMap();
let activeWatcher = void 0;
function onWatcherCleanup(cleanupFn, failSilently = false, owner = activeWatcher) {
  if (owner) {
    let cleanups = cleanupMap.get(owner);
    if (!cleanups) cleanupMap.set(owner, cleanups = []);
    cleanups.push(cleanupFn);
  }
}
function watch$1(source, cb, options = EMPTY_OBJ) {
  const { immediate, deep, once, scheduler, augmentJob, call } = options;
  const reactiveGetter = (source2) => {
    if (deep) return source2;
    if (/* @__PURE__ */ isShallow(source2) || deep === false || deep === 0)
      return traverse(source2, 1);
    return traverse(source2);
  };
  let effect2;
  let getter;
  let cleanup;
  let boundCleanup;
  let forceTrigger = false;
  let isMultiSource = false;
  if (/* @__PURE__ */ isRef(source)) {
    getter = () => source.value;
    forceTrigger = /* @__PURE__ */ isShallow(source);
  } else if (/* @__PURE__ */ isReactive(source)) {
    getter = () => reactiveGetter(source);
    forceTrigger = true;
  } else if (isArray(source)) {
    isMultiSource = true;
    forceTrigger = source.some((s) => /* @__PURE__ */ isReactive(s) || /* @__PURE__ */ isShallow(s));
    getter = () => source.map((s) => {
      if (/* @__PURE__ */ isRef(s)) {
        return s.value;
      } else if (/* @__PURE__ */ isReactive(s)) {
        return reactiveGetter(s);
      } else if (isFunction(s)) {
        return call ? call(s, 2) : s();
      } else ;
    });
  } else if (isFunction(source)) {
    if (cb) {
      getter = call ? () => call(source, 2) : source;
    } else {
      getter = () => {
        if (cleanup) {
          pauseTracking();
          try {
            cleanup();
          } finally {
            resetTracking();
          }
        }
        const currentEffect = activeWatcher;
        activeWatcher = effect2;
        try {
          return call ? call(source, 3, [boundCleanup]) : source(boundCleanup);
        } finally {
          activeWatcher = currentEffect;
        }
      };
    }
  } else {
    getter = NOOP;
  }
  if (cb && deep) {
    const baseGetter = getter;
    const depth = deep === true ? Infinity : deep;
    getter = () => traverse(baseGetter(), depth);
  }
  const scope = getCurrentScope();
  const watchHandle = () => {
    effect2.stop();
    if (scope && scope.active) {
      remove(scope.effects, effect2);
    }
  };
  if (once && cb) {
    const _cb = cb;
    cb = (...args) => {
      const res = _cb(...args);
      watchHandle();
      return res;
    };
  }
  let oldValue = isMultiSource ? new Array(source.length).fill(INITIAL_WATCHER_VALUE) : INITIAL_WATCHER_VALUE;
  const job = (immediateFirstRun) => {
    if (!(effect2.flags & 1) || !effect2.dirty && !immediateFirstRun) {
      return;
    }
    if (cb) {
      const newValue = effect2.run();
      if (immediateFirstRun || deep || forceTrigger || (isMultiSource ? newValue.some((v, i) => hasChanged(v, oldValue[i])) : hasChanged(newValue, oldValue))) {
        if (cleanup) {
          cleanup();
        }
        const currentWatcher = activeWatcher;
        activeWatcher = effect2;
        try {
          const args = [
            newValue,
            // pass undefined as the old value when it's changed for the first time
            oldValue === INITIAL_WATCHER_VALUE ? void 0 : isMultiSource && oldValue[0] === INITIAL_WATCHER_VALUE ? [] : oldValue,
            boundCleanup
          ];
          oldValue = newValue;
          call ? call(cb, 3, args) : (
            // @ts-expect-error
            cb(...args)
          );
        } finally {
          activeWatcher = currentWatcher;
        }
      }
    } else {
      effect2.run();
    }
  };
  if (augmentJob) {
    augmentJob(job);
  }
  effect2 = new ReactiveEffect(getter);
  effect2.scheduler = scheduler ? () => scheduler(job, false) : job;
  boundCleanup = (fn) => onWatcherCleanup(fn, false, effect2);
  cleanup = effect2.onStop = () => {
    const cleanups = cleanupMap.get(effect2);
    if (cleanups) {
      if (call) {
        call(cleanups, 4);
      } else {
        for (const cleanup2 of cleanups) cleanup2();
      }
      cleanupMap.delete(effect2);
    }
  };
  if (cb) {
    if (immediate) {
      job(true);
    } else {
      oldValue = effect2.run();
    }
  } else if (scheduler) {
    scheduler(job.bind(null, true), true);
  } else {
    effect2.run();
  }
  watchHandle.pause = effect2.pause.bind(effect2);
  watchHandle.resume = effect2.resume.bind(effect2);
  watchHandle.stop = watchHandle;
  return watchHandle;
}
function traverse(value, depth = Infinity, seen) {
  if (depth <= 0 || !isObject(value) || value["__v_skip"]) {
    return value;
  }
  seen = seen || /* @__PURE__ */ new Map();
  if ((seen.get(value) || 0) >= depth) {
    return value;
  }
  seen.set(value, depth);
  depth--;
  if (/* @__PURE__ */ isRef(value)) {
    traverse(value.value, depth, seen);
  } else if (isArray(value)) {
    for (let i = 0; i < value.length; i++) {
      traverse(value[i], depth, seen);
    }
  } else if (isSet(value) || isMap(value)) {
    value.forEach((v) => {
      traverse(v, depth, seen);
    });
  } else if (isPlainObject(value)) {
    for (const key in value) {
      traverse(value[key], depth, seen);
    }
    for (const key of Object.getOwnPropertySymbols(value)) {
      if (Object.prototype.propertyIsEnumerable.call(value, key)) {
        traverse(value[key], depth, seen);
      }
    }
  }
  return value;
}
/**
* @vue/runtime-core v3.5.41
* (c) 2018-present Yuxi (Evan) You and Vue contributors
* @license MIT
**/
const stack = [];
let isWarning = false;
function warn$1(msg, ...args) {
  if (isWarning) return;
  isWarning = true;
  pauseTracking();
  const instance = stack.length ? stack[stack.length - 1].component : null;
  const appWarnHandler = instance && instance.appContext.config.warnHandler;
  const trace = getComponentTrace();
  if (appWarnHandler) {
    callWithErrorHandling(
      appWarnHandler,
      instance,
      11,
      [
        // eslint-disable-next-line no-restricted-syntax
        msg + args.map((a) => {
          var _a, _b;
          return (_b = (_a = a.toString) == null ? void 0 : _a.call(a)) != null ? _b : JSON.stringify(a);
        }).join(""),
        instance && instance.proxy,
        trace.map(
          ({ vnode }) => `at <${formatComponentName(instance, vnode.type)}>`
        ).join("\n"),
        trace
      ]
    );
  } else {
    const warnArgs = [`[Vue warn]: ${msg}`, ...args];
    if (trace.length && // avoid spamming console during tests
    true) {
      warnArgs.push(`
`, ...formatTrace(trace));
    }
    console.warn(...warnArgs);
  }
  resetTracking();
  isWarning = false;
}
function getComponentTrace() {
  let currentVNode = stack[stack.length - 1];
  if (!currentVNode) {
    return [];
  }
  const normalizedStack = [];
  while (currentVNode) {
    const last = normalizedStack[0];
    if (last && last.vnode === currentVNode) {
      last.recurseCount++;
    } else {
      normalizedStack.push({
        vnode: currentVNode,
        recurseCount: 0
      });
    }
    const parentInstance = currentVNode.component && currentVNode.component.parent;
    currentVNode = parentInstance && parentInstance.vnode;
  }
  return normalizedStack;
}
function formatTrace(trace) {
  const logs = [];
  trace.forEach((entry, i) => {
    logs.push(...i === 0 ? [] : [`
`], ...formatTraceEntry(entry));
  });
  return logs;
}
function formatTraceEntry({ vnode, recurseCount }) {
  const postfix = recurseCount > 0 ? `... (${recurseCount} recursive calls)` : ``;
  const isRoot = vnode.component ? vnode.component.parent == null : false;
  const open = ` at <${formatComponentName(
    vnode.component,
    vnode.type,
    isRoot
  )}`;
  const close = `>` + postfix;
  return vnode.props ? [open, ...formatProps(vnode.props), close] : [open + close];
}
function formatProps(props) {
  const res = [];
  const keys = Object.keys(props);
  keys.slice(0, 3).forEach((key) => {
    res.push(...formatProp(key, props[key]));
  });
  if (keys.length > 3) {
    res.push(` ...`);
  }
  return res;
}
function formatProp(key, value, raw) {
  if (isString(value)) {
    value = JSON.stringify(value);
    return raw ? value : [`${key}=${value}`];
  } else if (typeof value === "number" || typeof value === "boolean" || value == null) {
    return raw ? value : [`${key}=${value}`];
  } else if (/* @__PURE__ */ isRef(value)) {
    value = formatProp(key, /* @__PURE__ */ toRaw(value.value), true);
    return raw ? value : [`${key}=Ref<`, value, `>`];
  } else if (isFunction(value)) {
    return [`${key}=fn${value.name ? `<${value.name}>` : ``}`];
  } else {
    value = /* @__PURE__ */ toRaw(value);
    return raw ? value : [`${key}=`, value];
  }
}
function callWithErrorHandling(fn, instance, type, args) {
  try {
    return args ? fn(...args) : fn();
  } catch (err) {
    handleError(err, instance, type);
  }
}
function callWithAsyncErrorHandling(fn, instance, type, args) {
  if (isFunction(fn)) {
    const res = callWithErrorHandling(fn, instance, type, args);
    if (res && isPromise(res)) {
      res.catch((err) => {
        handleError(err, instance, type);
      });
    }
    return res;
  }
  if (isArray(fn)) {
    const values = [];
    for (let i = 0; i < fn.length; i++) {
      values.push(callWithAsyncErrorHandling(fn[i], instance, type, args));
    }
    return values;
  }
}
function handleError(err, instance, type, throwInDev = true) {
  const contextVNode = instance ? instance.vnode : null;
  const { errorHandler, throwUnhandledErrorInProduction } = instance && instance.appContext.config || EMPTY_OBJ;
  if (instance) {
    let cur = instance.parent;
    const exposedInstance = instance.proxy;
    const errorInfo = `https://vuejs.org/error-reference/#runtime-${type}`;
    while (cur) {
      const errorCapturedHooks = cur.ec;
      if (errorCapturedHooks) {
        for (let i = 0; i < errorCapturedHooks.length; i++) {
          if (errorCapturedHooks[i](err, exposedInstance, errorInfo) === false) {
            return;
          }
        }
      }
      cur = cur.parent;
    }
    if (errorHandler) {
      pauseTracking();
      callWithErrorHandling(errorHandler, null, 10, [
        err,
        exposedInstance,
        errorInfo
      ]);
      resetTracking();
      return;
    }
  }
  logError(err, type, contextVNode, throwInDev, throwUnhandledErrorInProduction);
}
function logError(err, type, contextVNode, throwInDev = true, throwInProd = false) {
  if (throwInProd) {
    throw err;
  } else {
    console.error(err);
  }
}
const queue = [];
let flushIndex = -1;
const pendingPostFlushCbs = [];
let activePostFlushCbs = null;
let postFlushIndex = 0;
const resolvedPromise = /* @__PURE__ */ Promise.resolve();
let currentFlushPromise = null;
function nextTick(fn) {
  const p2 = currentFlushPromise || resolvedPromise;
  return fn ? p2.then(this ? fn.bind(this) : fn) : p2;
}
function findInsertionIndex(id) {
  let start = flushIndex + 1;
  let end = queue.length;
  while (start < end) {
    const middle = start + end >>> 1;
    const middleJob = queue[middle];
    const middleJobId = getId(middleJob);
    if (middleJobId < id || middleJobId === id && middleJob.flags & 2) {
      start = middle + 1;
    } else {
      end = middle;
    }
  }
  return start;
}
function queueJob(job) {
  if (!(job.flags & 1)) {
    const jobId = getId(job);
    const lastJob = queue[queue.length - 1];
    if (!lastJob || // fast path when the job id is larger than the tail
    !(job.flags & 2) && jobId >= getId(lastJob)) {
      queue.push(job);
    } else {
      queue.splice(findInsertionIndex(jobId), 0, job);
    }
    job.flags |= 1;
    queueFlush();
  }
}
function queueFlush() {
  if (!currentFlushPromise) {
    currentFlushPromise = resolvedPromise.then(flushJobs);
  }
}
function queuePostFlushCb(cb) {
  if (!isArray(cb)) {
    if (activePostFlushCbs && cb.id === -1) {
      activePostFlushCbs.splice(postFlushIndex + 1, 0, cb);
    } else if (!(cb.flags & 1)) {
      pendingPostFlushCbs.push(cb);
      cb.flags |= 1;
    }
  } else {
    for (let i = 0; i < cb.length; i++) {
      pendingPostFlushCbs.push(cb[i]);
    }
  }
  queueFlush();
}
function flushPreFlushCbs(instance, seen, i = flushIndex + 1) {
  for (; i < queue.length; i++) {
    const cb = queue[i];
    if (cb && cb.flags & 2) {
      if (instance && cb.id !== instance.uid) {
        continue;
      }
      queue.splice(i, 1);
      i--;
      if (cb.flags & 4) {
        cb.flags &= -2;
      }
      cb();
      if (!(cb.flags & 4)) {
        cb.flags &= -2;
      }
    }
  }
}
function flushPostFlushCbs(seen) {
  if (pendingPostFlushCbs.length) {
    const deduped = [...new Set(pendingPostFlushCbs)].sort(
      (a, b) => getId(a) - getId(b)
    );
    pendingPostFlushCbs.length = 0;
    if (activePostFlushCbs) {
      for (let i = 0; i < deduped.length; i++) {
        activePostFlushCbs.push(deduped[i]);
      }
      return;
    }
    activePostFlushCbs = deduped;
    for (postFlushIndex = 0; postFlushIndex < activePostFlushCbs.length; postFlushIndex++) {
      const cb = activePostFlushCbs[postFlushIndex];
      if (cb.flags & 4) {
        cb.flags &= -2;
      }
      if (!(cb.flags & 8)) cb();
      cb.flags &= -2;
    }
    activePostFlushCbs = null;
    postFlushIndex = 0;
  }
}
const getId = (job) => job.id == null ? job.flags & 2 ? -1 : Infinity : job.id;
function flushJobs(seen) {
  try {
    for (flushIndex = 0; flushIndex < queue.length; flushIndex++) {
      const job = queue[flushIndex];
      if (job && !(job.flags & 8)) {
        if (false) ;
        if (job.flags & 4) {
          job.flags &= ~1;
        }
        callWithErrorHandling(
          job,
          job.i,
          job.i ? 15 : 14
        );
        if (!(job.flags & 4)) {
          job.flags &= ~1;
        }
      }
    }
  } finally {
    for (; flushIndex < queue.length; flushIndex++) {
      const job = queue[flushIndex];
      if (job) {
        job.flags &= -2;
      }
    }
    flushIndex = -1;
    queue.length = 0;
    flushPostFlushCbs();
    currentFlushPromise = null;
    if (queue.length || pendingPostFlushCbs.length) {
      flushJobs();
    }
  }
}
let currentRenderingInstance = null;
let currentScopeId = null;
function setCurrentRenderingInstance(instance) {
  const prev = currentRenderingInstance;
  currentRenderingInstance = instance;
  currentScopeId = instance && instance.type.__scopeId || null;
  return prev;
}
function withCtx(fn, ctx = currentRenderingInstance, isNonScopedSlot) {
  if (!ctx) return fn;
  if (fn._n) {
    return fn;
  }
  const renderFnWithContext = (...args) => {
    if (renderFnWithContext._d) {
      setBlockTracking(-1);
    }
    const prevInstance = setCurrentRenderingInstance(ctx);
    const prevStackSize = blockStack.length;
    let res;
    try {
      res = fn(...args);
    } finally {
      for (let i = blockStack.length; i > prevStackSize; i--) closeBlock();
      setCurrentRenderingInstance(prevInstance);
      if (renderFnWithContext._d) {
        setBlockTracking(1);
      }
    }
    return res;
  };
  renderFnWithContext._n = true;
  renderFnWithContext._c = true;
  renderFnWithContext._d = true;
  return renderFnWithContext;
}
function withDirectives(vnode, directives) {
  if (currentRenderingInstance === null) {
    return vnode;
  }
  const instance = getComponentPublicInstance(currentRenderingInstance);
  const bindings = vnode.dirs || (vnode.dirs = []);
  for (let i = 0; i < directives.length; i++) {
    let [dir, value, arg, modifiers = EMPTY_OBJ] = directives[i];
    if (dir) {
      if (isFunction(dir)) {
        dir = {
          mounted: dir,
          updated: dir
        };
      }
      if (dir.deep) {
        traverse(value);
      }
      bindings.push({
        dir,
        instance,
        value,
        oldValue: void 0,
        arg,
        modifiers
      });
    }
  }
  return vnode;
}
function invokeDirectiveHook(vnode, prevVNode, instance, name) {
  const bindings = vnode.dirs;
  const oldBindings = prevVNode && prevVNode.dirs;
  for (let i = 0; i < bindings.length; i++) {
    const binding = bindings[i];
    if (oldBindings) {
      binding.oldValue = oldBindings[i].value;
    }
    let hook = binding.dir[name];
    if (hook) {
      pauseTracking();
      callWithAsyncErrorHandling(hook, instance, 8, [
        vnode.el,
        binding,
        vnode,
        prevVNode
      ]);
      resetTracking();
    }
  }
}
function provide(key, value) {
  if (currentInstance) {
    let provides = currentInstance.provides;
    const parentProvides = currentInstance.parent && currentInstance.parent.provides;
    if (parentProvides === provides) {
      provides = currentInstance.provides = Object.create(parentProvides);
    }
    provides[key] = value;
  }
}
function inject(key, defaultValue, treatDefaultAsFactory = false) {
  const instance = getCurrentInstance();
  if (instance || currentApp) {
    let provides = currentApp ? currentApp._context.provides : instance ? instance.parent == null || instance.ce ? instance.vnode.appContext && instance.vnode.appContext.provides : instance.parent.provides : void 0;
    if (provides && key in provides) {
      return provides[key];
    } else if (arguments.length > 1) {
      return treatDefaultAsFactory && isFunction(defaultValue) ? defaultValue.call(instance && instance.proxy) : defaultValue;
    } else ;
  }
}
const ssrContextKey = /* @__PURE__ */ Symbol.for("v-scx");
const useSSRContext = () => {
  {
    const ctx = inject(ssrContextKey);
    return ctx;
  }
};
function watch(source, cb, options) {
  return doWatch(source, cb, options);
}
function doWatch(source, cb, options = EMPTY_OBJ) {
  const { immediate, deep, flush, once } = options;
  const baseWatchOptions = extend({}, options);
  const runsImmediately = cb && immediate || !cb && flush !== "post";
  let ssrCleanup;
  if (isInSSRComponentSetup) {
    if (flush === "sync") {
      const ctx = useSSRContext();
      ssrCleanup = ctx.__watcherHandles || (ctx.__watcherHandles = []);
    } else if (!runsImmediately) {
      const watchStopHandle = () => {
      };
      watchStopHandle.stop = NOOP;
      watchStopHandle.resume = NOOP;
      watchStopHandle.pause = NOOP;
      return watchStopHandle;
    }
  }
  const instance = currentInstance;
  baseWatchOptions.call = (fn, type, args) => callWithAsyncErrorHandling(fn, instance, type, args);
  let isPre = false;
  if (flush === "post") {
    baseWatchOptions.scheduler = (job) => {
      queuePostRenderEffect(job, instance && instance.suspense);
    };
  } else if (flush !== "sync") {
    isPre = true;
    baseWatchOptions.scheduler = (job, isFirstRun) => {
      if (isFirstRun) {
        job();
      } else {
        queueJob(job);
      }
    };
  }
  baseWatchOptions.augmentJob = (job) => {
    if (cb) {
      job.flags |= 4;
    }
    if (isPre) {
      job.flags |= 2;
      if (instance) {
        job.id = instance.uid;
        job.i = instance;
      }
    }
  };
  const watchHandle = watch$1(source, cb, baseWatchOptions);
  if (isInSSRComponentSetup) {
    if (ssrCleanup) {
      ssrCleanup.push(watchHandle);
    } else if (runsImmediately) {
      watchHandle();
    }
  }
  return watchHandle;
}
function instanceWatch(source, value, options) {
  const publicThis = this.proxy;
  const getter = isString(source) ? source.includes(".") ? createPathGetter(publicThis, source) : () => publicThis[source] : source.bind(publicThis, publicThis);
  let cb;
  if (isFunction(value)) {
    cb = value;
  } else {
    cb = value.handler;
    options = value;
  }
  const reset = setCurrentInstance(this);
  const res = doWatch(getter, cb.bind(publicThis), options);
  reset();
  return res;
}
function createPathGetter(ctx, path) {
  const segments = path.split(".");
  return () => {
    let cur = ctx;
    for (let i = 0; i < segments.length && cur; i++) {
      cur = cur[segments[i]];
    }
    return cur;
  };
}
const TeleportEndKey = /* @__PURE__ */ Symbol("_vte");
const isTeleport = (type) => type.__isTeleport;
const leaveCbKey = /* @__PURE__ */ Symbol("_leaveCb");
function findNonCommentChild(children) {
  let child = children[0];
  if (children.length > 1) {
    for (const c of children) {
      if (c.type !== Comment) {
        child = c;
        break;
      }
    }
  }
  return child;
}
function getInnerChild$1(vnode) {
  if (!isKeepAlive(vnode)) {
    if (isTeleport(vnode.type) && vnode.children) {
      return findNonCommentChild(vnode.children);
    }
    return vnode;
  }
  if (vnode.component) {
    return vnode.component.subTree;
  }
  const { shapeFlag, children } = vnode;
  if (children) {
    if (shapeFlag & 16) {
      return children[0];
    }
    if (shapeFlag & 32 && isFunction(children.default)) {
      return children.default();
    }
  }
}
function setTransitionHooks(vnode, hooks) {
  if (vnode.shapeFlag & 6 && vnode.component) {
    vnode.transition = hooks;
    const subTree = vnode.component.subTree;
    setTransitionHooks(
      isTeleport(subTree.type) ? getInnerChild$1(subTree) || subTree : subTree,
      hooks
    );
  } else if (vnode.shapeFlag & 128) {
    vnode.ssContent.transition = hooks.clone(vnode.ssContent);
    vnode.ssFallback.transition = hooks.clone(vnode.ssFallback);
  } else {
    vnode.transition = hooks;
  }
}
function markAsyncBoundary(instance) {
  instance.ids = [instance.ids[0] + instance.ids[2]++ + "-", 0, 0];
}
function isTemplateRefKey(refs, key) {
  let desc;
  return !!((desc = Object.getOwnPropertyDescriptor(refs, key)) && !desc.configurable);
}
const pendingSetRefMap = /* @__PURE__ */ new WeakMap();
function setRef(rawRef, oldRawRef, parentSuspense, vnode, isUnmount = false) {
  if (isArray(rawRef)) {
    rawRef.forEach(
      (r, i) => setRef(
        r,
        oldRawRef && (isArray(oldRawRef) ? oldRawRef[i] : oldRawRef),
        parentSuspense,
        vnode,
        isUnmount
      )
    );
    return;
  }
  if (isAsyncWrapper(vnode) && !isUnmount) {
    if (vnode.shapeFlag & 512 && vnode.type.__asyncResolved && vnode.component.subTree.component) {
      setRef(rawRef, oldRawRef, parentSuspense, vnode.component.subTree);
    }
    return;
  }
  const refValue = vnode.shapeFlag & 4 ? getComponentPublicInstance(vnode.component) : vnode.el;
  const value = isUnmount ? null : refValue;
  const { i: owner, r: ref3 } = rawRef;
  const oldRef = oldRawRef && oldRawRef.r;
  const refs = owner.refs === EMPTY_OBJ ? owner.refs = {} : owner.refs;
  const setupState = owner.setupState;
  const rawSetupState = /* @__PURE__ */ toRaw(setupState);
  const canSetSetupRef = setupState === EMPTY_OBJ ? NO : (key) => {
    if (isTemplateRefKey(refs, key)) {
      return false;
    }
    return hasOwn(rawSetupState, key);
  };
  const canSetRef = (ref22, key) => {
    if (key && isTemplateRefKey(refs, key)) {
      return false;
    }
    return true;
  };
  if (oldRef != null && oldRef !== ref3) {
    invalidatePendingSetRef(oldRawRef);
    if (isString(oldRef)) {
      refs[oldRef] = null;
      if (canSetSetupRef(oldRef)) {
        setupState[oldRef] = null;
      }
    } else if (/* @__PURE__ */ isRef(oldRef)) {
      const oldRawRefAtom = oldRawRef;
      if (canSetRef(oldRef, oldRawRefAtom.k)) {
        oldRef.value = null;
      }
      if (oldRawRefAtom.k) refs[oldRawRefAtom.k] = null;
    }
  }
  if (isFunction(ref3)) {
    callWithErrorHandling(ref3, owner, 12, [value, refs]);
  } else {
    const _isString = isString(ref3);
    const _isRef = /* @__PURE__ */ isRef(ref3);
    if (_isString || _isRef) {
      const doSet = () => {
        if (rawRef.f) {
          const existing = _isString ? canSetSetupRef(ref3) ? setupState[ref3] : refs[ref3] : canSetRef() || !rawRef.k ? ref3.value : refs[rawRef.k];
          if (isUnmount) {
            isArray(existing) && remove(existing, refValue);
          } else {
            if (!isArray(existing)) {
              if (_isString) {
                refs[ref3] = [refValue];
                if (canSetSetupRef(ref3)) {
                  setupState[ref3] = refs[ref3];
                }
              } else {
                const newVal = [refValue];
                if (canSetRef(ref3, rawRef.k)) {
                  ref3.value = newVal;
                }
                if (rawRef.k) refs[rawRef.k] = newVal;
              }
            } else if (!existing.includes(refValue)) {
              existing.push(refValue);
            }
          }
        } else if (_isString) {
          refs[ref3] = value;
          if (canSetSetupRef(ref3)) {
            setupState[ref3] = value;
          }
        } else if (_isRef) {
          if (canSetRef(ref3, rawRef.k)) {
            ref3.value = value;
          }
          if (rawRef.k) refs[rawRef.k] = value;
        } else ;
      };
      if (value) {
        const job = () => {
          doSet();
          pendingSetRefMap.delete(rawRef);
        };
        job.id = -1;
        pendingSetRefMap.set(rawRef, job);
        queuePostRenderEffect(job, parentSuspense);
      } else {
        invalidatePendingSetRef(rawRef);
        doSet();
      }
    }
  }
}
function invalidatePendingSetRef(rawRef) {
  const pendingSetRef = pendingSetRefMap.get(rawRef);
  if (pendingSetRef) {
    pendingSetRef.flags |= 8;
    pendingSetRefMap.delete(rawRef);
  }
}
getGlobalThis().requestIdleCallback || ((cb) => setTimeout(cb, 1));
getGlobalThis().cancelIdleCallback || ((id) => clearTimeout(id));
const isAsyncWrapper = (i) => !!i.type.__asyncLoader;
const isKeepAlive = (vnode) => vnode.type.__isKeepAlive;
function onActivated(hook, target) {
  registerKeepAliveHook(hook, "a", target);
}
function onDeactivated(hook, target) {
  registerKeepAliveHook(hook, "da", target);
}
function registerKeepAliveHook(hook, type, target = currentInstance) {
  const wrappedHook = hook.__wdc || (hook.__wdc = () => {
    let current = target;
    while (current) {
      if (current.isDeactivated) {
        return;
      }
      current = current.parent;
    }
    return hook();
  });
  injectHook(type, wrappedHook, target);
  if (target) {
    let current = target.parent;
    while (current && current.parent) {
      if (isKeepAlive(current.parent.vnode)) {
        injectToKeepAliveRoot(wrappedHook, type, target, current);
      }
      current = current.parent;
    }
  }
}
function injectToKeepAliveRoot(hook, type, target, keepAliveRoot) {
  const injected = injectHook(
    type,
    hook,
    keepAliveRoot,
    true
    /* prepend */
  );
  onUnmounted(() => {
    remove(keepAliveRoot[type], injected);
  }, target);
}
function injectHook(type, hook, target = currentInstance, prepend = false) {
  if (target) {
    const hooks = target[type] || (target[type] = []);
    const wrappedHook = hook.__weh || (hook.__weh = (...args) => {
      pauseTracking();
      const reset = setCurrentInstance(target);
      const res = callWithAsyncErrorHandling(hook, target, type, args);
      reset();
      resetTracking();
      return res;
    });
    if (prepend) {
      hooks.unshift(wrappedHook);
    } else {
      hooks.push(wrappedHook);
    }
    return wrappedHook;
  }
}
const createHook = (lifecycle) => (hook, target = currentInstance) => {
  if (!isInSSRComponentSetup || lifecycle === "sp") {
    injectHook(lifecycle, (...args) => hook(...args), target);
  }
};
const onBeforeMount = createHook("bm");
const onMounted = createHook("m");
const onBeforeUpdate = createHook(
  "bu"
);
const onUpdated = createHook("u");
const onBeforeUnmount = createHook(
  "bum"
);
const onUnmounted = createHook("um");
const onServerPrefetch = createHook(
  "sp"
);
const onRenderTriggered = createHook("rtg");
const onRenderTracked = createHook("rtc");
function onErrorCaptured(hook, target = currentInstance) {
  injectHook("ec", hook, target);
}
const COMPONENTS = "components";
const NULL_DYNAMIC_COMPONENT = /* @__PURE__ */ Symbol.for("v-ndc");
function resolveDynamicComponent(component) {
  if (isString(component)) {
    return resolveAsset(COMPONENTS, component, false) || component;
  } else {
    return component || NULL_DYNAMIC_COMPONENT;
  }
}
function resolveAsset(type, name, warnMissing = true, maybeSelfReference = false) {
  const instance = currentRenderingInstance || currentInstance;
  if (instance) {
    const Component = instance.type;
    {
      const selfName = getComponentName(
        Component,
        false
      );
      if (selfName && (selfName === name || selfName === camelize(name) || selfName === capitalize(camelize(name)))) {
        return Component;
      }
    }
    const res = (
      // local registration
      // check instance[type] first which is resolved for options API
      resolve(instance[type] || Component[type], name) || // global registration
      resolve(instance.appContext[type], name)
    );
    if (!res && maybeSelfReference) {
      return Component;
    }
    return res;
  }
}
function resolve(registry, name) {
  return registry && (registry[name] || registry[camelize(name)] || registry[capitalize(camelize(name))]);
}
function renderList(source, renderItem, cache, index) {
  let ret;
  const cached = cache;
  const sourceIsArray = isArray(source);
  if (sourceIsArray || isString(source)) {
    const sourceIsReactiveArray = sourceIsArray && /* @__PURE__ */ isReactive(source);
    let needsWrap = false;
    let isReadonlySource = false;
    if (sourceIsReactiveArray) {
      needsWrap = !/* @__PURE__ */ isShallow(source);
      isReadonlySource = /* @__PURE__ */ isReadonly(source);
      source = shallowReadArray(source);
    }
    ret = new Array(source.length);
    for (let i = 0, l = source.length; i < l; i++) {
      ret[i] = renderItem(
        needsWrap ? isReadonlySource ? toReadonly(toReactive(source[i])) : toReactive(source[i]) : source[i],
        i,
        void 0,
        cached
      );
    }
  } else if (typeof source === "number") {
    {
      ret = new Array(source);
      for (let i = 0; i < source; i++) {
        ret[i] = renderItem(i + 1, i, void 0, cached);
      }
    }
  } else if (isObject(source)) {
    if (source[Symbol.iterator]) {
      ret = Array.from(
        source,
        (item, i) => renderItem(item, i, void 0, cached)
      );
    } else {
      const keys = Object.keys(source);
      ret = new Array(keys.length);
      for (let i = 0, l = keys.length; i < l; i++) {
        const key = keys[i];
        ret[i] = renderItem(source[key], key, i, cached);
      }
    }
  } else {
    ret = [];
  }
  return ret;
}
const getPublicInstance = (i) => {
  if (!i) return null;
  if (isStatefulComponent(i)) return getComponentPublicInstance(i);
  return getPublicInstance(i.parent);
};
const publicPropertiesMap = (
  // Move PURE marker to new line to workaround compiler discarding it
  // due to type annotation
  /* @__PURE__ */ extend(/* @__PURE__ */ Object.create(null), {
    $: (i) => i,
    $el: (i) => i.vnode.el,
    $data: (i) => i.data,
    $props: (i) => i.props,
    $attrs: (i) => i.attrs,
    $slots: (i) => i.slots,
    $refs: (i) => i.refs,
    $parent: (i) => getPublicInstance(i.parent),
    $root: (i) => getPublicInstance(i.root),
    $host: (i) => i.ce,
    $emit: (i) => i.emit,
    $options: (i) => resolveMergedOptions(i),
    $forceUpdate: (i) => i.f || (i.f = () => {
      queueJob(i.update);
    }),
    $nextTick: (i) => i.n || (i.n = nextTick.bind(i.proxy)),
    $watch: (i) => instanceWatch.bind(i)
  })
);
const hasSetupBinding = (state, key) => state !== EMPTY_OBJ && !state.__isScriptSetup && hasOwn(state, key);
const PublicInstanceProxyHandlers = {
  get({ _: instance }, key) {
    if (key === "__v_skip") {
      return true;
    }
    const { ctx, setupState, data, props, accessCache, type, appContext } = instance;
    if (key[0] !== "$") {
      const n = accessCache[key];
      if (n !== void 0) {
        switch (n) {
          case 1:
            return setupState[key];
          case 2:
            return data[key];
          case 4:
            return ctx[key];
          case 3:
            return props[key];
        }
      } else if (hasSetupBinding(setupState, key)) {
        accessCache[key] = 1;
        return setupState[key];
      } else if (data !== EMPTY_OBJ && hasOwn(data, key)) {
        accessCache[key] = 2;
        return data[key];
      } else if (hasOwn(props, key)) {
        accessCache[key] = 3;
        return props[key];
      } else if (ctx !== EMPTY_OBJ && hasOwn(ctx, key)) {
        accessCache[key] = 4;
        return ctx[key];
      } else if (shouldCacheAccess) {
        accessCache[key] = 0;
      }
    }
    const publicGetter = publicPropertiesMap[key];
    let cssModule, globalProperties;
    if (publicGetter) {
      if (key === "$attrs") {
        track(instance.attrs, "get", "");
      }
      return publicGetter(instance);
    } else if (
      // css module (injected by vue-loader)
      (cssModule = type.__cssModules) && (cssModule = cssModule[key])
    ) {
      return cssModule;
    } else if (ctx !== EMPTY_OBJ && hasOwn(ctx, key)) {
      accessCache[key] = 4;
      return ctx[key];
    } else if (
      // global properties
      globalProperties = appContext.config.globalProperties, hasOwn(globalProperties, key)
    ) {
      {
        return globalProperties[key];
      }
    } else ;
  },
  set({ _: instance }, key, value) {
    const { data, setupState, ctx } = instance;
    if (hasSetupBinding(setupState, key)) {
      setupState[key] = value;
      return true;
    } else if (data !== EMPTY_OBJ && hasOwn(data, key)) {
      data[key] = value;
      return true;
    } else if (hasOwn(instance.props, key)) {
      return false;
    }
    if (key[0] === "$" && key.slice(1) in instance) {
      return false;
    } else {
      {
        ctx[key] = value;
      }
    }
    return true;
  },
  has({
    _: { data, setupState, accessCache, ctx, appContext, props, type }
  }, key) {
    let cssModules;
    return !!(accessCache[key] || data !== EMPTY_OBJ && key[0] !== "$" && hasOwn(data, key) || hasSetupBinding(setupState, key) || hasOwn(props, key) || hasOwn(ctx, key) || hasOwn(publicPropertiesMap, key) || hasOwn(appContext.config.globalProperties, key) || (cssModules = type.__cssModules) && cssModules[key]);
  },
  defineProperty(target, key, descriptor) {
    if (descriptor.get != null) {
      target._.accessCache[key] = 0;
    } else if (hasOwn(descriptor, "value")) {
      this.set(target, key, descriptor.value, null);
    }
    return Reflect.defineProperty(target, key, descriptor);
  }
};
function normalizePropsOrEmits(props) {
  return isArray(props) ? props.reduce(
    (normalized, p2) => (normalized[p2] = null, normalized),
    {}
  ) : props;
}
let shouldCacheAccess = true;
function applyOptions(instance) {
  const options = resolveMergedOptions(instance);
  const publicThis = instance.proxy;
  const ctx = instance.ctx;
  shouldCacheAccess = false;
  if (options.beforeCreate) {
    callHook(options.beforeCreate, instance, "bc");
  }
  const {
    // state
    data: dataOptions,
    computed: computedOptions,
    methods,
    watch: watchOptions,
    provide: provideOptions,
    inject: injectOptions,
    // lifecycle
    created,
    beforeMount,
    mounted,
    beforeUpdate,
    updated,
    activated,
    deactivated,
    beforeDestroy,
    beforeUnmount,
    destroyed,
    unmounted,
    render,
    renderTracked,
    renderTriggered,
    errorCaptured,
    serverPrefetch,
    // public API
    expose,
    inheritAttrs,
    // assets
    components,
    directives,
    filters
  } = options;
  const checkDuplicateProperties = null;
  if (injectOptions) {
    resolveInjections(injectOptions, ctx, checkDuplicateProperties);
  }
  if (methods) {
    for (const key in methods) {
      const methodHandler = methods[key];
      if (isFunction(methodHandler)) {
        {
          ctx[key] = methodHandler.bind(publicThis);
        }
      }
    }
  }
  if (dataOptions) {
    const data = dataOptions.call(publicThis, publicThis);
    if (!isObject(data)) ;
    else {
      instance.data = /* @__PURE__ */ reactive(data);
    }
  }
  shouldCacheAccess = true;
  if (computedOptions) {
    for (const key in computedOptions) {
      const opt = computedOptions[key];
      const get = isFunction(opt) ? opt.bind(publicThis, publicThis) : isFunction(opt.get) ? opt.get.bind(publicThis, publicThis) : NOOP;
      const set = !isFunction(opt) && isFunction(opt.set) ? opt.set.bind(publicThis) : NOOP;
      const c = computed({
        get,
        set
      });
      Object.defineProperty(ctx, key, {
        enumerable: true,
        configurable: true,
        get: () => c.value,
        set: (v) => c.value = v
      });
    }
  }
  if (watchOptions) {
    for (const key in watchOptions) {
      createWatcher(watchOptions[key], ctx, publicThis, key);
    }
  }
  if (provideOptions) {
    const provides = isFunction(provideOptions) ? provideOptions.call(publicThis) : provideOptions;
    Reflect.ownKeys(provides).forEach((key) => {
      provide(key, provides[key]);
    });
  }
  if (created) {
    callHook(created, instance, "c");
  }
  function registerLifecycleHook(register, hook) {
    if (isArray(hook)) {
      hook.forEach((_hook) => register(_hook.bind(publicThis)));
    } else if (hook) {
      register(hook.bind(publicThis));
    }
  }
  registerLifecycleHook(onBeforeMount, beforeMount);
  registerLifecycleHook(onMounted, mounted);
  registerLifecycleHook(onBeforeUpdate, beforeUpdate);
  registerLifecycleHook(onUpdated, updated);
  registerLifecycleHook(onActivated, activated);
  registerLifecycleHook(onDeactivated, deactivated);
  registerLifecycleHook(onErrorCaptured, errorCaptured);
  registerLifecycleHook(onRenderTracked, renderTracked);
  registerLifecycleHook(onRenderTriggered, renderTriggered);
  registerLifecycleHook(onBeforeUnmount, beforeUnmount);
  registerLifecycleHook(onUnmounted, unmounted);
  registerLifecycleHook(onServerPrefetch, serverPrefetch);
  if (isArray(expose)) {
    if (expose.length) {
      const exposed = instance.exposed || (instance.exposed = {});
      expose.forEach((key) => {
        Object.defineProperty(exposed, key, {
          get: () => publicThis[key],
          set: (val) => publicThis[key] = val,
          enumerable: true
        });
      });
    } else if (!instance.exposed) {
      instance.exposed = {};
    }
  }
  if (render && instance.render === NOOP) {
    instance.render = render;
  }
  if (inheritAttrs != null) {
    instance.inheritAttrs = inheritAttrs;
  }
  if (components) instance.components = components;
  if (directives) instance.directives = directives;
  if (serverPrefetch) {
    markAsyncBoundary(instance);
  }
}
function resolveInjections(injectOptions, ctx, checkDuplicateProperties = NOOP) {
  if (isArray(injectOptions)) {
    injectOptions = normalizeInject(injectOptions);
  }
  for (const key in injectOptions) {
    const opt = injectOptions[key];
    let injected;
    if (isObject(opt)) {
      if ("default" in opt) {
        injected = inject(
          opt.from || key,
          opt.default,
          true
        );
      } else {
        injected = inject(opt.from || key);
      }
    } else {
      injected = inject(opt);
    }
    if (/* @__PURE__ */ isRef(injected)) {
      Object.defineProperty(ctx, key, {
        enumerable: true,
        configurable: true,
        get: () => injected.value,
        set: (v) => injected.value = v
      });
    } else {
      ctx[key] = injected;
    }
  }
}
function callHook(hook, instance, type) {
  callWithAsyncErrorHandling(
    isArray(hook) ? hook.map((h2) => h2.bind(instance.proxy)) : hook.bind(instance.proxy),
    instance,
    type
  );
}
function createWatcher(raw, ctx, publicThis, key) {
  let getter = key.includes(".") ? createPathGetter(publicThis, key) : () => publicThis[key];
  if (isString(raw)) {
    const handler = ctx[raw];
    if (isFunction(handler)) {
      {
        watch(getter, handler);
      }
    }
  } else if (isFunction(raw)) {
    {
      watch(getter, raw.bind(publicThis));
    }
  } else if (isObject(raw)) {
    if (isArray(raw)) {
      raw.forEach((r) => createWatcher(r, ctx, publicThis, key));
    } else {
      const handler = isFunction(raw.handler) ? raw.handler.bind(publicThis) : ctx[raw.handler];
      if (isFunction(handler)) {
        watch(getter, handler, raw);
      }
    }
  } else ;
}
function resolveMergedOptions(instance) {
  const base = instance.type;
  const { mixins, extends: extendsOptions } = base;
  const {
    mixins: globalMixins,
    optionsCache: cache,
    config: { optionMergeStrategies }
  } = instance.appContext;
  const cached = cache.get(base);
  let resolved;
  if (cached) {
    resolved = cached;
  } else if (!globalMixins.length && !mixins && !extendsOptions) {
    {
      resolved = base;
    }
  } else {
    resolved = {};
    if (globalMixins.length) {
      globalMixins.forEach(
        (m) => mergeOptions(resolved, m, optionMergeStrategies, true)
      );
    }
    mergeOptions(resolved, base, optionMergeStrategies);
  }
  if (isObject(base)) {
    cache.set(base, resolved);
  }
  return resolved;
}
function mergeOptions(to, from, strats, asMixin = false) {
  const { mixins, extends: extendsOptions } = from;
  if (extendsOptions) {
    mergeOptions(to, extendsOptions, strats, true);
  }
  if (mixins) {
    mixins.forEach(
      (m) => mergeOptions(to, m, strats, true)
    );
  }
  for (const key in from) {
    if (asMixin && key === "expose") ;
    else {
      const strat = internalOptionMergeStrats[key] || strats && strats[key];
      to[key] = strat ? strat(to[key], from[key]) : from[key];
    }
  }
  return to;
}
const internalOptionMergeStrats = {
  data: mergeDataFn,
  props: mergeEmitsOrPropsOptions,
  emits: mergeEmitsOrPropsOptions,
  // objects
  methods: mergeObjectOptions,
  computed: mergeObjectOptions,
  // lifecycle
  beforeCreate: mergeAsArray,
  created: mergeAsArray,
  beforeMount: mergeAsArray,
  mounted: mergeAsArray,
  beforeUpdate: mergeAsArray,
  updated: mergeAsArray,
  beforeDestroy: mergeAsArray,
  beforeUnmount: mergeAsArray,
  destroyed: mergeAsArray,
  unmounted: mergeAsArray,
  activated: mergeAsArray,
  deactivated: mergeAsArray,
  errorCaptured: mergeAsArray,
  serverPrefetch: mergeAsArray,
  // assets
  components: mergeObjectOptions,
  directives: mergeObjectOptions,
  // watch
  watch: mergeWatchOptions,
  // provide / inject
  provide: mergeDataFn,
  inject: mergeInject
};
function mergeDataFn(to, from) {
  if (!from) {
    return to;
  }
  if (!to) {
    return from;
  }
  return function mergedDataFn() {
    return extend(
      isFunction(to) ? to.call(this, this) : to,
      isFunction(from) ? from.call(this, this) : from
    );
  };
}
function mergeInject(to, from) {
  return mergeObjectOptions(normalizeInject(to), normalizeInject(from));
}
function normalizeInject(raw) {
  if (isArray(raw)) {
    const res = {};
    for (let i = 0; i < raw.length; i++) {
      res[raw[i]] = raw[i];
    }
    return res;
  }
  return raw;
}
function mergeAsArray(to, from) {
  return to ? [...new Set([].concat(to, from))] : from;
}
function mergeObjectOptions(to, from) {
  return to ? extend(/* @__PURE__ */ Object.create(null), to, from) : from;
}
function mergeEmitsOrPropsOptions(to, from) {
  if (to) {
    if (isArray(to) && isArray(from)) {
      return [.../* @__PURE__ */ new Set([...to, ...from])];
    }
    return extend(
      /* @__PURE__ */ Object.create(null),
      normalizePropsOrEmits(to),
      normalizePropsOrEmits(from != null ? from : {})
    );
  } else {
    return from;
  }
}
function mergeWatchOptions(to, from) {
  if (!to) return from;
  if (!from) return to;
  const merged = extend(/* @__PURE__ */ Object.create(null), to);
  for (const key in from) {
    merged[key] = mergeAsArray(to[key], from[key]);
  }
  return merged;
}
function createAppContext() {
  return {
    app: null,
    config: {
      isNativeTag: NO,
      performance: false,
      globalProperties: {},
      optionMergeStrategies: {},
      errorHandler: void 0,
      warnHandler: void 0,
      compilerOptions: {}
    },
    mixins: [],
    components: {},
    directives: {},
    provides: /* @__PURE__ */ Object.create(null),
    optionsCache: /* @__PURE__ */ new WeakMap(),
    propsCache: /* @__PURE__ */ new WeakMap(),
    emitsCache: /* @__PURE__ */ new WeakMap()
  };
}
let uid$1 = 0;
function createAppAPI(render, hydrate) {
  return function createApp2(rootComponent, rootProps = null) {
    if (!isFunction(rootComponent)) {
      rootComponent = extend({}, rootComponent);
    }
    if (rootProps != null && !isObject(rootProps)) {
      rootProps = null;
    }
    const context = createAppContext();
    const installedPlugins = /* @__PURE__ */ new WeakSet();
    const pluginCleanupFns = [];
    let isMounted = false;
    const app = context.app = {
      _uid: uid$1++,
      _component: rootComponent,
      _props: rootProps,
      _container: null,
      _context: context,
      _instance: null,
      version,
      get config() {
        return context.config;
      },
      set config(v) {
      },
      use(plugin, ...options) {
        if (installedPlugins.has(plugin)) ;
        else if (plugin && isFunction(plugin.install)) {
          installedPlugins.add(plugin);
          plugin.install(app, ...options);
        } else if (isFunction(plugin)) {
          installedPlugins.add(plugin);
          plugin(app, ...options);
        } else ;
        return app;
      },
      mixin(mixin) {
        {
          if (!context.mixins.includes(mixin)) {
            context.mixins.push(mixin);
          }
        }
        return app;
      },
      component(name, component) {
        if (!component) {
          return context.components[name];
        }
        context.components[name] = component;
        return app;
      },
      directive(name, directive) {
        if (!directive) {
          return context.directives[name];
        }
        context.directives[name] = directive;
        return app;
      },
      mount(rootContainer, isHydrate, namespace) {
        if (!isMounted) {
          const vnode = app._ceVNode || createVNode(rootComponent, rootProps);
          vnode.appContext = context;
          if (namespace === true) {
            namespace = "svg";
          } else if (namespace === false) {
            namespace = void 0;
          }
          {
            render(vnode, rootContainer, namespace);
          }
          isMounted = true;
          app._container = rootContainer;
          rootContainer.__vue_app__ = app;
          return getComponentPublicInstance(vnode.component);
        }
      },
      onUnmount(cleanupFn) {
        pluginCleanupFns.push(cleanupFn);
      },
      unmount() {
        if (isMounted) {
          callWithAsyncErrorHandling(
            pluginCleanupFns,
            app._instance,
            16
          );
          render(null, app._container);
          delete app._container.__vue_app__;
        }
      },
      provide(key, value) {
        context.provides[key] = value;
        return app;
      },
      runWithContext(fn) {
        const lastApp = currentApp;
        currentApp = app;
        try {
          return fn();
        } finally {
          currentApp = lastApp;
        }
      }
    };
    return app;
  };
}
let currentApp = null;
const getModelModifiers = (props, modelName) => {
  return modelName === "modelValue" || modelName === "model-value" ? props.modelModifiers : props[`${modelName}Modifiers`] || props[`${camelize(modelName)}Modifiers`] || props[`${hyphenate(modelName)}Modifiers`];
};
function emit(instance, event, ...rawArgs) {
  if (instance.isUnmounted) return;
  const props = instance.vnode.props || EMPTY_OBJ;
  let args = rawArgs;
  const isModelListener2 = event.startsWith("update:");
  const modifiers = isModelListener2 && getModelModifiers(props, event.slice(7));
  if (modifiers) {
    if (modifiers.trim) {
      args = rawArgs.map((a) => isString(a) ? a.trim() : a);
    }
    if (modifiers.number) {
      args = rawArgs.map(looseToNumber);
    }
  }
  let handlerName;
  let handler = props[handlerName = toHandlerKey(event)] || // also try camelCase event handler (#2249)
  props[handlerName = toHandlerKey(camelize(event))];
  if (!handler && isModelListener2) {
    handler = props[handlerName = toHandlerKey(hyphenate(event))];
  }
  if (handler) {
    callWithAsyncErrorHandling(
      handler,
      instance,
      6,
      args
    );
  }
  const onceHandler = props[handlerName + `Once`];
  if (onceHandler) {
    if (!instance.emitted) {
      instance.emitted = {};
    } else if (instance.emitted[handlerName]) {
      return;
    }
    instance.emitted[handlerName] = true;
    callWithAsyncErrorHandling(
      onceHandler,
      instance,
      6,
      args
    );
  }
}
const mixinEmitsCache = /* @__PURE__ */ new WeakMap();
function normalizeEmitsOptions(comp, appContext, asMixin = false) {
  const cache = asMixin ? mixinEmitsCache : appContext.emitsCache;
  const cached = cache.get(comp);
  if (cached !== void 0) {
    return cached;
  }
  const raw = comp.emits;
  let normalized = {};
  let hasExtends = false;
  if (!isFunction(comp)) {
    const extendEmits = (raw2) => {
      const normalizedFromExtend = normalizeEmitsOptions(raw2, appContext, true);
      if (normalizedFromExtend) {
        hasExtends = true;
        extend(normalized, normalizedFromExtend);
      }
    };
    if (!asMixin && appContext.mixins.length) {
      appContext.mixins.forEach(extendEmits);
    }
    if (comp.extends) {
      extendEmits(comp.extends);
    }
    if (comp.mixins) {
      comp.mixins.forEach(extendEmits);
    }
  }
  if (!raw && !hasExtends) {
    if (isObject(comp)) {
      cache.set(comp, null);
    }
    return null;
  }
  if (isArray(raw)) {
    raw.forEach((key) => normalized[key] = null);
  } else {
    extend(normalized, raw);
  }
  if (isObject(comp)) {
    cache.set(comp, normalized);
  }
  return normalized;
}
function isEmitListener(options, key) {
  if (!options || !isOn(key)) {
    return false;
  }
  key = key.slice(2);
  key = key === "Once" ? key : key.replace(/Once$/, "");
  return hasOwn(options, key[0].toLowerCase() + key.slice(1)) || hasOwn(options, hyphenate(key)) || hasOwn(options, key);
}
function markAttrsAccessed() {
}
function renderComponentRoot(instance) {
  const {
    type: Component,
    vnode,
    proxy,
    withProxy,
    propsOptions: [propsOptions],
    slots,
    attrs,
    emit: emit2,
    render,
    renderCache,
    props,
    data,
    setupState,
    ctx,
    inheritAttrs
  } = instance;
  const prev = setCurrentRenderingInstance(instance);
  let result;
  let fallthroughAttrs;
  try {
    if (vnode.shapeFlag & 4) {
      const proxyToUse = withProxy || proxy;
      const thisProxy = false ? new Proxy(proxyToUse, {
        get(target, key, receiver) {
          warn$1(
            `Property '${String(
              key
            )}' was accessed via 'this'. Avoid using 'this' in templates.`
          );
          return Reflect.get(target, key, receiver);
        }
      }) : proxyToUse;
      result = normalizeVNode(
        render.call(
          thisProxy,
          proxyToUse,
          renderCache,
          false ? /* @__PURE__ */ shallowReadonly(props) : props,
          setupState,
          data,
          ctx
        )
      );
      fallthroughAttrs = attrs;
    } else {
      const render2 = Component;
      if (false) ;
      result = normalizeVNode(
        render2.length > 1 ? render2(
          false ? /* @__PURE__ */ shallowReadonly(props) : props,
          false ? {
            get attrs() {
              markAttrsAccessed();
              return /* @__PURE__ */ shallowReadonly(attrs);
            },
            slots,
            emit: emit2
          } : { attrs, slots, emit: emit2 }
        ) : render2(
          false ? /* @__PURE__ */ shallowReadonly(props) : props,
          null
        )
      );
      fallthroughAttrs = Component.props ? attrs : getFunctionalFallthrough(attrs);
    }
  } catch (err) {
    blockStack.length = 0;
    handleError(err, instance, 1);
    result = createVNode(Comment);
  }
  let root = result;
  if (fallthroughAttrs && inheritAttrs !== false) {
    const keys = Object.keys(fallthroughAttrs);
    const { shapeFlag } = root;
    if (keys.length) {
      if (shapeFlag & (1 | 6)) {
        if (propsOptions && keys.some(isModelListener)) {
          fallthroughAttrs = filterModelListeners(
            fallthroughAttrs,
            propsOptions
          );
        }
        root = cloneVNode(root, fallthroughAttrs, false, true);
      }
    }
  }
  if (vnode.dirs) {
    root = cloneVNode(root, null, false, true);
    root.dirs = root.dirs ? root.dirs.concat(vnode.dirs) : vnode.dirs;
  }
  if (vnode.transition) {
    const child = isTeleport(root.type) ? getInnerChild$1(root) || root : root;
    setTransitionHooks(child, vnode.transition);
  }
  {
    result = root;
  }
  setCurrentRenderingInstance(prev);
  return result;
}
const getFunctionalFallthrough = (attrs) => {
  let res;
  for (const key in attrs) {
    if (key === "class" || key === "style" || isOn(key)) {
      (res || (res = {}))[key] = attrs[key];
    }
  }
  return res;
};
const filterModelListeners = (attrs, props) => {
  const res = {};
  for (const key in attrs) {
    if (!isModelListener(key) || !(key.slice(9) in props)) {
      res[key] = attrs[key];
    }
  }
  return res;
};
function shouldUpdateComponent(prevVNode, nextVNode, optimized) {
  const { props: prevProps, children: prevChildren, component } = prevVNode;
  const { props: nextProps, children: nextChildren, patchFlag } = nextVNode;
  const emits = component.emitsOptions;
  if (nextVNode.dirs || nextVNode.transition) {
    return true;
  }
  if (optimized && patchFlag >= 0) {
    if (patchFlag & 1024) {
      return true;
    }
    if (patchFlag & 16) {
      if (!prevProps) {
        return !!nextProps;
      }
      return hasPropsChanged(prevProps, nextProps, emits);
    } else if (patchFlag & 8) {
      const dynamicProps = nextVNode.dynamicProps;
      for (let i = 0; i < dynamicProps.length; i++) {
        const key = dynamicProps[i];
        if (hasPropValueChanged(nextProps, prevProps, key) && !isEmitListener(emits, key)) {
          return true;
        }
      }
    }
  } else {
    if (prevChildren || nextChildren) {
      if (!nextChildren || !nextChildren.$stable) {
        return true;
      }
    }
    if (prevProps === nextProps) {
      return false;
    }
    if (!prevProps) {
      return !!nextProps;
    }
    if (!nextProps) {
      return true;
    }
    return hasPropsChanged(prevProps, nextProps, emits);
  }
  return false;
}
function hasPropsChanged(prevProps, nextProps, emitsOptions) {
  const nextKeys = Object.keys(nextProps);
  if (nextKeys.length !== Object.keys(prevProps).length) {
    return true;
  }
  for (let i = 0; i < nextKeys.length; i++) {
    const key = nextKeys[i];
    if (hasPropValueChanged(nextProps, prevProps, key) && !isEmitListener(emitsOptions, key)) {
      return true;
    }
  }
  return false;
}
function hasPropValueChanged(nextProps, prevProps, key) {
  const nextProp = nextProps[key];
  const prevProp = prevProps[key];
  if (key === "style" && isObject(nextProp) && isObject(prevProp)) {
    return !looseEqual(nextProp, prevProp);
  }
  return nextProp !== prevProp;
}
function updateHOCHostEl({ vnode, parent, suspense }, el) {
  while (parent) {
    const root = parent.subTree;
    if (root.suspense && root.suspense.activeBranch === vnode) {
      root.suspense.vnode.el = root.el = el;
      vnode = root;
    }
    if (root === vnode) {
      (vnode = parent.vnode).el = el;
      parent = parent.parent;
    } else {
      break;
    }
  }
  if (suspense && suspense.activeBranch === vnode) {
    suspense.vnode.el = el;
  }
}
const internalObjectProto = {};
const createInternalObject = () => Object.create(internalObjectProto);
const isInternalObject = (obj) => Object.getPrototypeOf(obj) === internalObjectProto;
function initProps(instance, rawProps, isStateful, isSSR = false) {
  const props = {};
  const attrs = createInternalObject();
  instance.propsDefaults = /* @__PURE__ */ Object.create(null);
  setFullProps(instance, rawProps, props, attrs);
  for (const key in instance.propsOptions[0]) {
    if (!(key in props)) {
      props[key] = void 0;
    }
  }
  if (isStateful) {
    instance.props = isSSR ? props : /* @__PURE__ */ shallowReactive(props);
  } else {
    if (!instance.type.props) {
      instance.props = attrs;
    } else {
      instance.props = props;
    }
  }
  instance.attrs = attrs;
}
function updateProps(instance, rawProps, rawPrevProps, optimized) {
  const {
    props,
    attrs,
    vnode: { patchFlag }
  } = instance;
  const rawCurrentProps = /* @__PURE__ */ toRaw(props);
  const [options] = instance.propsOptions;
  let hasAttrsChanged = false;
  if (
    // always force full diff in dev
    // - #1942 if hmr is enabled with sfc component
    // - vite#872 non-sfc component used by sfc component
    (optimized || patchFlag > 0) && !(patchFlag & 16)
  ) {
    if (patchFlag & 8) {
      const propsToUpdate = instance.vnode.dynamicProps;
      for (let i = 0; i < propsToUpdate.length; i++) {
        let key = propsToUpdate[i];
        if (isEmitListener(instance.emitsOptions, key)) {
          continue;
        }
        const value = rawProps[key];
        if (options) {
          if (hasOwn(attrs, key)) {
            if (value !== attrs[key]) {
              attrs[key] = value;
              hasAttrsChanged = true;
            }
          } else {
            const camelizedKey = camelize(key);
            props[camelizedKey] = resolvePropValue(
              options,
              rawCurrentProps,
              camelizedKey,
              value,
              instance,
              false
            );
          }
        } else {
          if (value !== attrs[key]) {
            attrs[key] = value;
            hasAttrsChanged = true;
          }
        }
      }
    }
  } else {
    if (setFullProps(instance, rawProps, props, attrs)) {
      hasAttrsChanged = true;
    }
    let kebabKey;
    for (const key in rawCurrentProps) {
      if (!rawProps || // for camelCase
      !hasOwn(rawProps, key) && // it's possible the original props was passed in as kebab-case
      // and converted to camelCase (#955)
      ((kebabKey = hyphenate(key)) === key || !hasOwn(rawProps, kebabKey))) {
        if (options) {
          if (rawPrevProps && // for camelCase
          (rawPrevProps[key] !== void 0 || // for kebab-case
          rawPrevProps[kebabKey] !== void 0)) {
            props[key] = resolvePropValue(
              options,
              rawCurrentProps,
              key,
              void 0,
              instance,
              true
            );
          }
        } else {
          delete props[key];
        }
      }
    }
    if (attrs !== rawCurrentProps) {
      for (const key in attrs) {
        if (!rawProps || !hasOwn(rawProps, key) && true) {
          delete attrs[key];
          hasAttrsChanged = true;
        }
      }
    }
  }
  if (hasAttrsChanged) {
    trigger(instance.attrs, "set", "");
  }
}
function setFullProps(instance, rawProps, props, attrs) {
  const [options, needCastKeys] = instance.propsOptions;
  let hasAttrsChanged = false;
  let rawCastValues;
  if (rawProps) {
    for (let key in rawProps) {
      if (isReservedProp(key)) {
        continue;
      }
      const value = rawProps[key];
      let camelKey;
      if (options && hasOwn(options, camelKey = camelize(key))) {
        if (!needCastKeys || !needCastKeys.includes(camelKey)) {
          props[camelKey] = value;
        } else {
          (rawCastValues || (rawCastValues = {}))[camelKey] = value;
        }
      } else if (!isEmitListener(instance.emitsOptions, key)) {
        if (!(key in attrs) || value !== attrs[key]) {
          attrs[key] = value;
          hasAttrsChanged = true;
        }
      }
    }
  }
  if (needCastKeys) {
    const rawCurrentProps = /* @__PURE__ */ toRaw(props);
    const castValues = rawCastValues || EMPTY_OBJ;
    for (let i = 0; i < needCastKeys.length; i++) {
      const key = needCastKeys[i];
      props[key] = resolvePropValue(
        options,
        rawCurrentProps,
        key,
        castValues[key],
        instance,
        !hasOwn(castValues, key)
      );
    }
  }
  return hasAttrsChanged;
}
function resolvePropValue(options, props, key, value, instance, isAbsent) {
  const opt = options[key];
  if (opt != null) {
    const hasDefault = hasOwn(opt, "default");
    if (hasDefault && value === void 0) {
      const defaultValue = opt.default;
      if (opt.type !== Function && !opt.skipFactory && isFunction(defaultValue)) {
        const { propsDefaults } = instance;
        if (key in propsDefaults) {
          value = propsDefaults[key];
        } else {
          const reset = setCurrentInstance(instance);
          value = propsDefaults[key] = defaultValue.call(
            null,
            props
          );
          reset();
        }
      } else {
        value = defaultValue;
      }
      if (instance.ce) {
        instance.ce._setProp(key, value);
      }
    }
    if (opt[
      0
      /* shouldCast */
    ]) {
      if (isAbsent && !hasDefault) {
        value = false;
      } else if (opt[
        1
        /* shouldCastTrue */
      ] && (value === "" || value === hyphenate(key))) {
        value = true;
      }
    }
  }
  return value;
}
const mixinPropsCache = /* @__PURE__ */ new WeakMap();
function normalizePropsOptions(comp, appContext, asMixin = false) {
  const cache = asMixin ? mixinPropsCache : appContext.propsCache;
  const cached = cache.get(comp);
  if (cached) {
    return cached;
  }
  const raw = comp.props;
  const normalized = {};
  const needCastKeys = [];
  let hasExtends = false;
  if (!isFunction(comp)) {
    const extendProps = (raw2) => {
      hasExtends = true;
      const [props, keys] = normalizePropsOptions(raw2, appContext, true);
      extend(normalized, props);
      if (keys) needCastKeys.push(...keys);
    };
    if (!asMixin && appContext.mixins.length) {
      appContext.mixins.forEach(extendProps);
    }
    if (comp.extends) {
      extendProps(comp.extends);
    }
    if (comp.mixins) {
      comp.mixins.forEach(extendProps);
    }
  }
  if (!raw && !hasExtends) {
    if (isObject(comp)) {
      cache.set(comp, EMPTY_ARR);
    }
    return EMPTY_ARR;
  }
  if (isArray(raw)) {
    for (let i = 0; i < raw.length; i++) {
      const normalizedKey = camelize(raw[i]);
      if (validatePropName(normalizedKey)) {
        normalized[normalizedKey] = EMPTY_OBJ;
      }
    }
  } else if (raw) {
    for (const key in raw) {
      const normalizedKey = camelize(key);
      if (validatePropName(normalizedKey)) {
        const opt = raw[key];
        const prop = normalized[normalizedKey] = isArray(opt) || isFunction(opt) ? { type: opt } : extend({}, opt);
        const propType = prop.type;
        let shouldCast = false;
        let shouldCastTrue = true;
        if (isArray(propType)) {
          for (let index = 0; index < propType.length; ++index) {
            const type = propType[index];
            const typeName = isFunction(type) && type.name;
            if (typeName === "Boolean") {
              shouldCast = true;
              break;
            } else if (typeName === "String") {
              shouldCastTrue = false;
            }
          }
        } else {
          shouldCast = isFunction(propType) && propType.name === "Boolean";
        }
        prop[
          0
          /* shouldCast */
        ] = shouldCast;
        prop[
          1
          /* shouldCastTrue */
        ] = shouldCastTrue;
        if (shouldCast || hasOwn(prop, "default")) {
          needCastKeys.push(normalizedKey);
        }
      }
    }
  }
  const res = [normalized, needCastKeys];
  if (isObject(comp)) {
    cache.set(comp, res);
  }
  return res;
}
function validatePropName(key) {
  if (key[0] !== "$" && !isReservedProp(key)) {
    return true;
  }
  return false;
}
const isInternalKey = (key) => key === "_" || key === "_ctx" || key === "$stable";
const normalizeSlotValue = (value) => isArray(value) ? value.map(normalizeVNode) : [normalizeVNode(value)];
const normalizeSlot = (key, rawSlot, ctx) => {
  if (rawSlot._n) {
    return rawSlot;
  }
  const normalized = withCtx((...args) => {
    if (false) ;
    return normalizeSlotValue(rawSlot(...args));
  }, ctx);
  normalized._c = false;
  return normalized;
};
const normalizeObjectSlots = (rawSlots, slots, instance) => {
  const ctx = rawSlots._ctx;
  for (const key in rawSlots) {
    if (isInternalKey(key)) continue;
    const value = rawSlots[key];
    if (isFunction(value)) {
      slots[key] = normalizeSlot(key, value, ctx);
    } else if (value != null) {
      const normalized = normalizeSlotValue(value);
      slots[key] = () => normalized;
    }
  }
};
const normalizeVNodeSlots = (instance, children) => {
  const normalized = normalizeSlotValue(children);
  instance.slots.default = () => normalized;
};
const assignSlots = (slots, children, optimized) => {
  for (const key in children) {
    if (optimized || !isInternalKey(key)) {
      slots[key] = children[key];
    }
  }
};
const initSlots = (instance, children, optimized) => {
  const slots = instance.slots = createInternalObject();
  if (instance.vnode.shapeFlag & 32) {
    const type = children._;
    if (type) {
      assignSlots(slots, children, optimized);
      if (optimized) {
        def(slots, "_", type, true);
      }
    } else {
      normalizeObjectSlots(children, slots);
    }
  } else if (children) {
    normalizeVNodeSlots(instance, children);
  }
};
const updateSlots = (instance, children, optimized) => {
  const { vnode, slots } = instance;
  let needDeletionCheck = true;
  let deletionComparisonTarget = EMPTY_OBJ;
  if (vnode.shapeFlag & 32) {
    const type = children._;
    if (type) {
      if (optimized && type === 1) {
        needDeletionCheck = false;
      } else {
        assignSlots(slots, children, optimized);
      }
    } else {
      needDeletionCheck = !children.$stable;
      normalizeObjectSlots(children, slots);
    }
    deletionComparisonTarget = children;
  } else if (children) {
    normalizeVNodeSlots(instance, children);
    deletionComparisonTarget = { default: 1 };
  }
  if (needDeletionCheck) {
    for (const key in slots) {
      if (!isInternalKey(key) && deletionComparisonTarget[key] == null) {
        delete slots[key];
      }
    }
  }
};
const queuePostRenderEffect = queueEffectWithSuspense;
function createRenderer(options) {
  return baseCreateRenderer(options);
}
function baseCreateRenderer(options, createHydrationFns) {
  const target = getGlobalThis();
  target.__VUE__ = true;
  const {
    insert: hostInsert,
    remove: hostRemove,
    patchProp: hostPatchProp,
    createElement: hostCreateElement,
    createText: hostCreateText,
    createComment: hostCreateComment,
    setText: hostSetText,
    setElementText: hostSetElementText,
    parentNode: hostParentNode,
    nextSibling: hostNextSibling,
    setScopeId: hostSetScopeId = NOOP,
    insertStaticContent: hostInsertStaticContent
  } = options;
  const patch = (n1, n2, container, anchor = null, parentComponent = null, parentSuspense = null, namespace = void 0, slotScopeIds = null, optimized = !!n2.dynamicChildren) => {
    if (n1 === n2) {
      return;
    }
    if (n1 && !isSameVNodeType(n1, n2)) {
      anchor = getNextHostNode(n1);
      unmount(n1, parentComponent, parentSuspense, true);
      n1 = null;
    }
    if (n2.patchFlag === -2) {
      optimized = false;
      n2.dynamicChildren = null;
    }
    const { type, ref: ref3, shapeFlag } = n2;
    switch (type) {
      case Text:
        processText(n1, n2, container, anchor);
        break;
      case Comment:
        processCommentNode(n1, n2, container, anchor);
        break;
      case Static:
        if (n1 == null) {
          mountStaticNode(n2, container, anchor, namespace);
        }
        break;
      case Fragment:
        processFragment(
          n1,
          n2,
          container,
          anchor,
          parentComponent,
          parentSuspense,
          namespace,
          slotScopeIds,
          optimized
        );
        break;
      default:
        if (shapeFlag & 1) {
          processElement(
            n1,
            n2,
            container,
            anchor,
            parentComponent,
            parentSuspense,
            namespace,
            slotScopeIds,
            optimized
          );
        } else if (shapeFlag & 6) {
          processComponent(
            n1,
            n2,
            container,
            anchor,
            parentComponent,
            parentSuspense,
            namespace,
            slotScopeIds,
            optimized
          );
        } else if (shapeFlag & 64) {
          type.process(
            n1,
            n2,
            container,
            anchor,
            parentComponent,
            parentSuspense,
            namespace,
            slotScopeIds,
            optimized,
            internals
          );
        } else if (shapeFlag & 128) {
          type.process(
            n1,
            n2,
            container,
            anchor,
            parentComponent,
            parentSuspense,
            namespace,
            slotScopeIds,
            optimized,
            internals
          );
        } else ;
    }
    if (ref3 != null && parentComponent) {
      setRef(ref3, n1 && n1.ref, parentSuspense, n2 || n1, !n2);
    } else if (ref3 == null && n1 && n1.ref != null) {
      setRef(n1.ref, null, parentSuspense, n1, true);
    }
  };
  const processText = (n1, n2, container, anchor) => {
    if (n1 == null) {
      hostInsert(
        n2.el = hostCreateText(n2.children),
        container,
        anchor
      );
    } else {
      const el = n2.el = n1.el;
      if (n2.children !== n1.children) {
        hostSetText(el, n2.children);
      }
    }
  };
  const processCommentNode = (n1, n2, container, anchor) => {
    if (n1 == null) {
      hostInsert(
        n2.el = hostCreateComment(n2.children || ""),
        container,
        anchor
      );
    } else {
      n2.el = n1.el;
    }
  };
  const mountStaticNode = (n2, container, anchor, namespace) => {
    [n2.el, n2.anchor] = hostInsertStaticContent(
      n2.children,
      container,
      anchor,
      namespace,
      n2.el,
      n2.anchor
    );
  };
  const moveStaticNode = ({ el, anchor }, container, nextSibling) => {
    let next;
    while (el && el !== anchor) {
      next = hostNextSibling(el);
      hostInsert(el, container, nextSibling);
      el = next;
    }
    hostInsert(anchor, container, nextSibling);
  };
  const removeStaticNode = ({ el, anchor }) => {
    let next;
    while (el && el !== anchor) {
      next = hostNextSibling(el);
      hostRemove(el);
      el = next;
    }
    hostRemove(anchor);
  };
  const processElement = (n1, n2, container, anchor, parentComponent, parentSuspense, namespace, slotScopeIds, optimized) => {
    if (n2.type === "svg") {
      namespace = "svg";
    } else if (n2.type === "math") {
      namespace = "mathml";
    }
    if (n1 == null) {
      mountElement(
        n2,
        container,
        anchor,
        parentComponent,
        parentSuspense,
        namespace,
        slotScopeIds,
        optimized
      );
    } else {
      const customElement = n1.el && n1.el._isVueCE ? n1.el : null;
      try {
        if (customElement) {
          customElement._beginPatch();
        }
        patchElement(
          n1,
          n2,
          parentComponent,
          parentSuspense,
          namespace,
          slotScopeIds,
          optimized
        );
      } finally {
        if (customElement) {
          customElement._endPatch();
        }
      }
    }
  };
  const mountElement = (vnode, container, anchor, parentComponent, parentSuspense, namespace, slotScopeIds, optimized) => {
    let el;
    let vnodeHook;
    const { props, shapeFlag, transition, dirs } = vnode;
    el = vnode.el = hostCreateElement(
      vnode.type,
      namespace,
      props && props.is,
      props
    );
    if (shapeFlag & 8) {
      hostSetElementText(el, vnode.children);
    } else if (shapeFlag & 16) {
      mountChildren(
        vnode.children,
        el,
        null,
        parentComponent,
        parentSuspense,
        resolveChildrenNamespace(vnode, namespace),
        slotScopeIds,
        optimized
      );
    }
    if (dirs) {
      invokeDirectiveHook(vnode, null, parentComponent, "created");
    }
    setScopeId(el, vnode, vnode.scopeId, slotScopeIds, parentComponent);
    if (props) {
      for (const key in props) {
        if (key !== "value" && !isReservedProp(key)) {
          hostPatchProp(el, key, null, props[key], namespace, parentComponent);
        }
      }
      if ("value" in props) {
        hostPatchProp(el, "value", null, props.value, namespace);
      }
      if (vnodeHook = props.onVnodeBeforeMount) {
        invokeVNodeHook(vnodeHook, parentComponent, vnode);
      }
    }
    if (dirs) {
      invokeDirectiveHook(vnode, null, parentComponent, "beforeMount");
    }
    const needCallTransitionHooks = needTransition(parentSuspense, transition);
    if (needCallTransitionHooks) {
      transition.beforeEnter(el);
    }
    hostInsert(el, container, anchor);
    if ((vnodeHook = props && props.onVnodeMounted) || needCallTransitionHooks || dirs) {
      queuePostRenderEffect(() => {
        try {
          vnodeHook && invokeVNodeHook(vnodeHook, parentComponent, vnode);
          needCallTransitionHooks && transition.enter(el);
          dirs && invokeDirectiveHook(vnode, null, parentComponent, "mounted");
        } finally {
        }
      }, parentSuspense);
    }
  };
  const setScopeId = (el, vnode, scopeId, slotScopeIds, parentComponent) => {
    if (scopeId) {
      hostSetScopeId(el, scopeId);
    }
    if (slotScopeIds) {
      for (let i = 0; i < slotScopeIds.length; i++) {
        hostSetScopeId(el, slotScopeIds[i]);
      }
    }
    if (parentComponent) {
      let subTree = parentComponent.subTree;
      if (vnode === subTree || isSuspense(subTree.type) && (subTree.ssContent === vnode || subTree.ssFallback === vnode)) {
        const parentVNode = parentComponent.vnode;
        setScopeId(
          el,
          parentVNode,
          parentVNode.scopeId,
          parentVNode.slotScopeIds,
          parentComponent.parent
        );
      }
    }
  };
  const mountChildren = (children, container, anchor, parentComponent, parentSuspense, namespace, slotScopeIds, optimized, start = 0) => {
    for (let i = start; i < children.length; i++) {
      const child = children[i] = optimized ? cloneIfMounted(children[i]) : normalizeVNode(children[i]);
      patch(
        null,
        child,
        container,
        anchor,
        parentComponent,
        parentSuspense,
        namespace,
        slotScopeIds,
        optimized
      );
    }
  };
  const patchElement = (n1, n2, parentComponent, parentSuspense, namespace, slotScopeIds, optimized) => {
    const el = n2.el = n1.el;
    let { patchFlag, dynamicChildren, dirs } = n2;
    patchFlag |= n1.patchFlag & 16;
    const oldProps = n1.props || EMPTY_OBJ;
    const newProps = n2.props || EMPTY_OBJ;
    let vnodeHook;
    parentComponent && toggleRecurse(parentComponent, false);
    if (vnodeHook = newProps.onVnodeBeforeUpdate) {
      invokeVNodeHook(vnodeHook, parentComponent, n2, n1);
    }
    if (dirs) {
      invokeDirectiveHook(n2, n1, parentComponent, "beforeUpdate");
    }
    parentComponent && toggleRecurse(parentComponent, true);
    if (
      // #6385 the old vnode may be a user-wrapped non-isomorphic block
      // Force full diff when block metadata is unstable.
      dynamicChildren && (!n1.dynamicChildren || n1.dynamicChildren.length !== dynamicChildren.length)
    ) {
      patchFlag = 0;
      optimized = false;
      dynamicChildren = null;
    }
    if (oldProps.innerHTML && newProps.innerHTML == null || oldProps.textContent && newProps.textContent == null) {
      hostSetElementText(el, "");
    }
    if (dynamicChildren) {
      patchBlockChildren(
        n1.dynamicChildren,
        dynamicChildren,
        el,
        parentComponent,
        parentSuspense,
        resolveChildrenNamespace(n2, namespace),
        slotScopeIds
      );
    } else if (!optimized) {
      patchChildren(
        n1,
        n2,
        el,
        null,
        parentComponent,
        parentSuspense,
        resolveChildrenNamespace(n2, namespace),
        slotScopeIds,
        false
      );
    }
    if (patchFlag > 0) {
      if (patchFlag & 16) {
        patchProps(el, oldProps, newProps, parentComponent, namespace);
      } else {
        if (patchFlag & 2) {
          if (oldProps.class !== newProps.class) {
            hostPatchProp(el, "class", null, newProps.class, namespace);
          }
        }
        if (patchFlag & 4) {
          hostPatchProp(el, "style", oldProps.style, newProps.style, namespace);
        }
        if (patchFlag & 8) {
          const propsToUpdate = n2.dynamicProps;
          for (let i = 0; i < propsToUpdate.length; i++) {
            const key = propsToUpdate[i];
            const prev = oldProps[key];
            const next = newProps[key];
            if (next !== prev || key === "value") {
              hostPatchProp(el, key, prev, next, namespace, parentComponent);
            }
          }
        }
      }
      if (patchFlag & 1) {
        if (n1.children !== n2.children) {
          hostSetElementText(el, n2.children);
        }
      }
    } else if (!optimized && dynamicChildren == null) {
      patchProps(el, oldProps, newProps, parentComponent, namespace);
    }
    if ((vnodeHook = newProps.onVnodeUpdated) || dirs) {
      queuePostRenderEffect(() => {
        vnodeHook && invokeVNodeHook(vnodeHook, parentComponent, n2, n1);
        dirs && invokeDirectiveHook(n2, n1, parentComponent, "updated");
      }, parentSuspense);
    }
  };
  const patchBlockChildren = (oldChildren, newChildren, fallbackContainer, parentComponent, parentSuspense, namespace, slotScopeIds) => {
    for (let i = 0; i < newChildren.length; i++) {
      const oldVNode = oldChildren[i];
      const newVNode = newChildren[i];
      const container = (
        // oldVNode may be an errored async setup() component inside Suspense
        // which will not have a mounted element
        oldVNode.el && // - In the case of a Fragment, we need to provide the actual parent
        // of the Fragment itself so it can move its children.
        (oldVNode.type === Fragment || // - In the case of different nodes, there is going to be a replacement
        // which also requires the correct parent container
        !isSameVNodeType(oldVNode, newVNode) || // - In the case of a component, it could contain anything.
        oldVNode.shapeFlag & (6 | 64 | 128)) ? hostParentNode(oldVNode.el) : (
          // In other cases, the parent container is not actually used so we
          // just pass the block element here to avoid a DOM parentNode call.
          fallbackContainer
        )
      );
      patch(
        oldVNode,
        newVNode,
        container,
        null,
        parentComponent,
        parentSuspense,
        namespace,
        slotScopeIds,
        true
      );
    }
  };
  const patchProps = (el, oldProps, newProps, parentComponent, namespace) => {
    if (oldProps !== newProps) {
      if (oldProps !== EMPTY_OBJ) {
        for (const key in oldProps) {
          if (!isReservedProp(key) && !(key in newProps)) {
            hostPatchProp(
              el,
              key,
              oldProps[key],
              null,
              namespace,
              parentComponent
            );
          }
        }
      }
      for (const key in newProps) {
        if (isReservedProp(key)) continue;
        const next = newProps[key];
        const prev = oldProps[key];
        if (next !== prev && key !== "value") {
          hostPatchProp(el, key, prev, next, namespace, parentComponent);
        }
      }
      if ("value" in newProps) {
        hostPatchProp(el, "value", oldProps.value, newProps.value, namespace);
      }
    }
  };
  const processFragment = (n1, n2, container, anchor, parentComponent, parentSuspense, namespace, slotScopeIds, optimized) => {
    const fragmentStartAnchor = n2.el = n1 ? n1.el : hostCreateText("");
    const fragmentEndAnchor = n2.anchor = n1 ? n1.anchor : hostCreateText("");
    let { patchFlag, dynamicChildren, slotScopeIds: fragmentSlotScopeIds } = n2;
    if (fragmentSlotScopeIds) {
      slotScopeIds = slotScopeIds ? slotScopeIds.concat(fragmentSlotScopeIds) : fragmentSlotScopeIds;
    }
    if (n1 == null) {
      hostInsert(fragmentStartAnchor, container, anchor);
      hostInsert(fragmentEndAnchor, container, anchor);
      mountChildren(
        // #10007
        // such fragment like `<></>` will be compiled into
        // a fragment which doesn't have a children.
        // In this case fallback to an empty array
        n2.children || [],
        container,
        fragmentEndAnchor,
        parentComponent,
        parentSuspense,
        namespace,
        slotScopeIds,
        optimized
      );
    } else {
      if (patchFlag > 0 && patchFlag & 64 && dynamicChildren && // #2715 the previous fragment could've been a BAILed one as a result
      // of renderSlot() with no valid children
      n1.dynamicChildren && n1.dynamicChildren.length === dynamicChildren.length) {
        patchBlockChildren(
          n1.dynamicChildren,
          dynamicChildren,
          container,
          parentComponent,
          parentSuspense,
          namespace,
          slotScopeIds
        );
        if (
          // #2080 if the stable fragment has a key, it's a <template v-for> that may
          //  get moved around. Make sure all root level vnodes inherit el.
          // #2134 or if it's a component root, it may also get moved around
          // as the component is being moved.
          n2.key != null || parentComponent && n2 === parentComponent.subTree
        ) {
          traverseStaticChildren(
            n1,
            n2,
            true
            /* shallow */
          );
        }
      } else {
        patchChildren(
          n1,
          n2,
          container,
          fragmentEndAnchor,
          parentComponent,
          parentSuspense,
          namespace,
          slotScopeIds,
          optimized
        );
      }
    }
  };
  const processComponent = (n1, n2, container, anchor, parentComponent, parentSuspense, namespace, slotScopeIds, optimized) => {
    n2.slotScopeIds = slotScopeIds;
    if (n1 == null) {
      if (n2.shapeFlag & 512) {
        parentComponent.ctx.activate(
          n2,
          container,
          anchor,
          namespace,
          optimized
        );
      } else {
        mountComponent(
          n2,
          container,
          anchor,
          parentComponent,
          parentSuspense,
          namespace,
          optimized
        );
      }
    } else {
      updateComponent(n1, n2, optimized);
    }
  };
  const mountComponent = (initialVNode, container, anchor, parentComponent, parentSuspense, namespace, optimized) => {
    const instance = initialVNode.component = createComponentInstance(
      initialVNode,
      parentComponent,
      parentSuspense
    );
    if (isKeepAlive(initialVNode)) {
      instance.ctx.renderer = internals;
    }
    {
      setupComponent(instance, false, optimized);
    }
    if (instance.asyncDep) {
      parentSuspense && parentSuspense.registerDep(instance, setupRenderEffect, optimized);
      if (!initialVNode.el) {
        const placeholder = instance.subTree = createVNode(Comment);
        processCommentNode(null, placeholder, container, anchor);
        initialVNode.placeholder = placeholder.el;
      }
    } else {
      setupRenderEffect(
        instance,
        initialVNode,
        container,
        anchor,
        parentSuspense,
        namespace,
        optimized
      );
    }
  };
  const updateComponent = (n1, n2, optimized) => {
    const instance = n2.component = n1.component;
    if (shouldUpdateComponent(n1, n2, optimized)) {
      if (instance.asyncDep && !instance.asyncResolved) {
        updateComponentPreRender(instance, n2, optimized);
        return;
      } else {
        instance.next = n2;
        instance.update();
      }
    } else {
      n2.el = n1.el;
      instance.vnode = n2;
    }
  };
  const setupRenderEffect = (instance, initialVNode, container, anchor, parentSuspense, namespace, optimized) => {
    const componentUpdateFn = () => {
      if (!instance.isMounted) {
        let vnodeHook;
        const { el, props } = initialVNode;
        const { bm, m, parent, root, type } = instance;
        const isAsyncWrapperVNode = isAsyncWrapper(initialVNode);
        toggleRecurse(instance, false);
        if (bm) {
          invokeArrayFns(bm);
        }
        if (!isAsyncWrapperVNode && (vnodeHook = props && props.onVnodeBeforeMount)) {
          invokeVNodeHook(vnodeHook, parent, initialVNode);
        }
        toggleRecurse(instance, true);
        {
          if (root.ce && root.ce._hasShadowRoot()) {
            root.ce._injectChildStyle(
              type,
              instance.parent ? instance.parent.type : void 0
            );
          }
          const subTree = instance.subTree = renderComponentRoot(instance);
          patch(
            null,
            subTree,
            container,
            anchor,
            instance,
            parentSuspense,
            namespace
          );
          initialVNode.el = subTree.el;
        }
        if (m) {
          queuePostRenderEffect(m, parentSuspense);
        }
        if (!isAsyncWrapperVNode && (vnodeHook = props && props.onVnodeMounted)) {
          const scopedInitialVNode = initialVNode;
          queuePostRenderEffect(
            () => invokeVNodeHook(vnodeHook, parent, scopedInitialVNode),
            parentSuspense
          );
        }
        if (initialVNode.shapeFlag & 256 || parent && isAsyncWrapper(parent.vnode) && parent.vnode.shapeFlag & 256) {
          instance.a && queuePostRenderEffect(instance.a, parentSuspense);
        }
        instance.isMounted = true;
        initialVNode = container = anchor = null;
      } else {
        let { next, bu, u, parent, vnode } = instance;
        {
          const nonHydratedAsyncRoot = locateNonHydratedAsyncRoot(instance);
          if (nonHydratedAsyncRoot) {
            if (next) {
              next.el = vnode.el;
              updateComponentPreRender(instance, next, optimized);
            }
            nonHydratedAsyncRoot.asyncDep.then(() => {
              queuePostRenderEffect(() => {
                if (!instance.isUnmounted) update();
              }, parentSuspense);
            });
            return;
          }
        }
        let originNext = next;
        let vnodeHook;
        toggleRecurse(instance, false);
        if (next) {
          next.el = vnode.el;
          updateComponentPreRender(instance, next, optimized);
        } else {
          next = vnode;
        }
        if (bu) {
          invokeArrayFns(bu);
        }
        if (vnodeHook = next.props && next.props.onVnodeBeforeUpdate) {
          invokeVNodeHook(vnodeHook, parent, next, vnode);
        }
        toggleRecurse(instance, true);
        const nextTree = renderComponentRoot(instance);
        const prevTree = instance.subTree;
        instance.subTree = nextTree;
        patch(
          prevTree,
          nextTree,
          // parent may have changed if it's in a teleport
          hostParentNode(prevTree.el),
          // anchor may have changed if it's in a fragment
          getNextHostNode(prevTree),
          instance,
          parentSuspense,
          namespace
        );
        next.el = nextTree.el;
        if (originNext === null) {
          updateHOCHostEl(instance, nextTree.el);
        }
        if (u) {
          queuePostRenderEffect(u, parentSuspense);
        }
        if (vnodeHook = next.props && next.props.onVnodeUpdated) {
          queuePostRenderEffect(
            () => invokeVNodeHook(vnodeHook, parent, next, vnode),
            parentSuspense
          );
        }
      }
    };
    instance.scope.on();
    const effect2 = instance.effect = new ReactiveEffect(componentUpdateFn);
    instance.scope.off();
    const update = instance.update = effect2.run.bind(effect2);
    const job = instance.job = effect2.runIfDirty.bind(effect2);
    job.i = instance;
    job.id = instance.uid;
    effect2.scheduler = () => queueJob(job);
    toggleRecurse(instance, true);
    update();
  };
  const updateComponentPreRender = (instance, nextVNode, optimized) => {
    nextVNode.component = instance;
    const prevProps = instance.vnode.props;
    instance.vnode = nextVNode;
    instance.next = null;
    updateProps(instance, nextVNode.props, prevProps, optimized);
    updateSlots(instance, nextVNode.children, optimized);
    pauseTracking();
    flushPreFlushCbs(instance);
    resetTracking();
  };
  const patchChildren = (n1, n2, container, anchor, parentComponent, parentSuspense, namespace, slotScopeIds, optimized = false) => {
    const c1 = n1 && n1.children;
    const prevShapeFlag = n1 ? n1.shapeFlag : 0;
    const c2 = n2.children;
    const { patchFlag, shapeFlag } = n2;
    if (patchFlag > 0) {
      if (patchFlag & 128) {
        patchKeyedChildren(
          c1,
          c2,
          container,
          anchor,
          parentComponent,
          parentSuspense,
          namespace,
          slotScopeIds,
          optimized
        );
        return;
      } else if (patchFlag & 256) {
        patchUnkeyedChildren(
          c1,
          c2,
          container,
          anchor,
          parentComponent,
          parentSuspense,
          namespace,
          slotScopeIds,
          optimized
        );
        return;
      }
    }
    if (shapeFlag & 8) {
      if (prevShapeFlag & 16) {
        unmountChildren(c1, parentComponent, parentSuspense);
      }
      if (c2 !== c1) {
        hostSetElementText(container, c2);
      }
    } else {
      if (prevShapeFlag & 16) {
        if (shapeFlag & 16) {
          patchKeyedChildren(
            c1,
            c2,
            container,
            anchor,
            parentComponent,
            parentSuspense,
            namespace,
            slotScopeIds,
            optimized
          );
        } else {
          unmountChildren(c1, parentComponent, parentSuspense, true);
        }
      } else {
        if (prevShapeFlag & 8) {
          hostSetElementText(container, "");
        }
        if (shapeFlag & 16) {
          mountChildren(
            c2,
            container,
            anchor,
            parentComponent,
            parentSuspense,
            namespace,
            slotScopeIds,
            optimized
          );
        }
      }
    }
  };
  const patchUnkeyedChildren = (c1, c2, container, anchor, parentComponent, parentSuspense, namespace, slotScopeIds, optimized) => {
    c1 = c1 || EMPTY_ARR;
    c2 = c2 || EMPTY_ARR;
    const oldLength = c1.length;
    const newLength = c2.length;
    const commonLength = Math.min(oldLength, newLength);
    let i;
    for (i = 0; i < commonLength; i++) {
      const nextChild = c2[i] = optimized ? cloneIfMounted(c2[i]) : normalizeVNode(c2[i]);
      patch(
        c1[i],
        nextChild,
        container,
        null,
        parentComponent,
        parentSuspense,
        namespace,
        slotScopeIds,
        optimized
      );
    }
    if (oldLength > newLength) {
      unmountChildren(
        c1,
        parentComponent,
        parentSuspense,
        true,
        false,
        commonLength
      );
    } else {
      mountChildren(
        c2,
        container,
        anchor,
        parentComponent,
        parentSuspense,
        namespace,
        slotScopeIds,
        optimized,
        commonLength
      );
    }
  };
  const patchKeyedChildren = (c1, c2, container, parentAnchor, parentComponent, parentSuspense, namespace, slotScopeIds, optimized) => {
    let i = 0;
    const l2 = c2.length;
    let e1 = c1.length - 1;
    let e2 = l2 - 1;
    while (i <= e1 && i <= e2) {
      const n1 = c1[i];
      const n2 = c2[i] = optimized ? cloneIfMounted(c2[i]) : normalizeVNode(c2[i]);
      if (isSameVNodeType(n1, n2)) {
        patch(
          n1,
          n2,
          container,
          null,
          parentComponent,
          parentSuspense,
          namespace,
          slotScopeIds,
          optimized
        );
      } else {
        break;
      }
      i++;
    }
    while (i <= e1 && i <= e2) {
      const n1 = c1[e1];
      const n2 = c2[e2] = optimized ? cloneIfMounted(c2[e2]) : normalizeVNode(c2[e2]);
      if (isSameVNodeType(n1, n2)) {
        patch(
          n1,
          n2,
          container,
          null,
          parentComponent,
          parentSuspense,
          namespace,
          slotScopeIds,
          optimized
        );
      } else {
        break;
      }
      e1--;
      e2--;
    }
    if (i > e1) {
      if (i <= e2) {
        const nextPos = e2 + 1;
        const anchor = nextPos < l2 ? c2[nextPos].el : parentAnchor;
        while (i <= e2) {
          patch(
            null,
            c2[i] = optimized ? cloneIfMounted(c2[i]) : normalizeVNode(c2[i]),
            container,
            anchor,
            parentComponent,
            parentSuspense,
            namespace,
            slotScopeIds,
            optimized
          );
          i++;
        }
      }
    } else if (i > e2) {
      while (i <= e1) {
        unmount(c1[i], parentComponent, parentSuspense, true);
        i++;
      }
    } else {
      const s1 = i;
      const s2 = i;
      const keyToNewIndexMap = /* @__PURE__ */ new Map();
      for (i = s2; i <= e2; i++) {
        const nextChild = c2[i] = optimized ? cloneIfMounted(c2[i]) : normalizeVNode(c2[i]);
        if (nextChild.key != null) {
          keyToNewIndexMap.set(nextChild.key, i);
        }
      }
      let j;
      let patched = 0;
      const toBePatched = e2 - s2 + 1;
      let moved = false;
      let maxNewIndexSoFar = 0;
      const newIndexToOldIndexMap = new Array(toBePatched);
      for (i = 0; i < toBePatched; i++) newIndexToOldIndexMap[i] = 0;
      for (i = s1; i <= e1; i++) {
        const prevChild = c1[i];
        if (patched >= toBePatched) {
          unmount(prevChild, parentComponent, parentSuspense, true);
          continue;
        }
        let newIndex;
        if (prevChild.key != null) {
          newIndex = keyToNewIndexMap.get(prevChild.key);
        } else {
          for (j = s2; j <= e2; j++) {
            if (newIndexToOldIndexMap[j - s2] === 0 && isSameVNodeType(prevChild, c2[j])) {
              newIndex = j;
              break;
            }
          }
        }
        if (newIndex === void 0) {
          unmount(prevChild, parentComponent, parentSuspense, true);
        } else {
          newIndexToOldIndexMap[newIndex - s2] = i + 1;
          if (newIndex >= maxNewIndexSoFar) {
            maxNewIndexSoFar = newIndex;
          } else {
            moved = true;
          }
          patch(
            prevChild,
            c2[newIndex],
            container,
            null,
            parentComponent,
            parentSuspense,
            namespace,
            slotScopeIds,
            optimized
          );
          patched++;
        }
      }
      const increasingNewIndexSequence = moved ? getSequence(newIndexToOldIndexMap) : EMPTY_ARR;
      j = increasingNewIndexSequence.length - 1;
      for (i = toBePatched - 1; i >= 0; i--) {
        const nextIndex = s2 + i;
        const nextChild = c2[nextIndex];
        const anchorVNode = c2[nextIndex + 1];
        const anchor = nextIndex + 1 < l2 ? (
          // #13559, #14173 fallback to el placeholder for unresolved async component
          anchorVNode.el || resolveAsyncComponentPlaceholder(anchorVNode)
        ) : parentAnchor;
        if (newIndexToOldIndexMap[i] === 0) {
          patch(
            null,
            nextChild,
            container,
            anchor,
            parentComponent,
            parentSuspense,
            namespace,
            slotScopeIds,
            optimized
          );
        } else if (moved) {
          if (j < 0 || i !== increasingNewIndexSequence[j]) {
            move(nextChild, container, anchor, 2);
          } else {
            j--;
          }
        }
      }
    }
  };
  const move = (vnode, container, anchor, moveType, parentSuspense = null) => {
    const { el, type, transition, children, shapeFlag } = vnode;
    if (shapeFlag & 6) {
      move(vnode.component.subTree, container, anchor, moveType);
      return;
    }
    if (shapeFlag & 128) {
      vnode.suspense.move(container, anchor, moveType);
      return;
    }
    if (shapeFlag & 64) {
      type.move(vnode, container, anchor, internals);
      return;
    }
    if (type === Fragment) {
      hostInsert(el, container, anchor);
      for (let i = 0; i < children.length; i++) {
        move(children[i], container, anchor, moveType);
      }
      hostInsert(vnode.anchor, container, anchor);
      return;
    }
    if (type === Static) {
      moveStaticNode(vnode, container, anchor);
      return;
    }
    const needTransition2 = moveType !== 2 && shapeFlag & 1 && transition;
    if (needTransition2) {
      if (moveType === 0) {
        if (transition.persisted && !el[leaveCbKey]) {
          hostInsert(el, container, anchor);
        } else {
          transition.beforeEnter(el);
          hostInsert(el, container, anchor);
          queuePostRenderEffect(() => transition.enter(el), parentSuspense);
        }
      } else {
        const { leave, delayLeave, afterLeave } = transition;
        const remove22 = () => {
          if (vnode.ctx.isUnmounted) {
            hostRemove(el);
          } else {
            hostInsert(el, container, anchor);
          }
        };
        const performLeave = () => {
          const wasLeaving = el._isLeaving || !!el[leaveCbKey];
          if (el._isLeaving) {
            el[leaveCbKey](
              true
              /* cancelled */
            );
          }
          if (transition.persisted && !wasLeaving) {
            remove22();
          } else {
            leave(el, () => {
              remove22();
              afterLeave && afterLeave();
            });
          }
        };
        if (delayLeave) {
          delayLeave(el, remove22, performLeave);
        } else {
          performLeave();
        }
      }
    } else {
      hostInsert(el, container, anchor);
    }
  };
  const unmount = (vnode, parentComponent, parentSuspense, doRemove = false, optimized = false) => {
    const {
      type,
      props,
      ref: ref3,
      children,
      dynamicChildren,
      shapeFlag,
      patchFlag,
      dirs,
      cacheIndex,
      memo
    } = vnode;
    if (patchFlag === -2) {
      optimized = false;
    }
    if (ref3 != null) {
      pauseTracking();
      setRef(ref3, null, parentSuspense, vnode, true);
      resetTracking();
    }
    if (cacheIndex != null) {
      parentComponent.renderCache[cacheIndex] = void 0;
    }
    if (shapeFlag & 256) {
      parentComponent.ctx.deactivate(vnode);
      return;
    }
    const shouldInvokeDirs = shapeFlag & 1 && dirs;
    const shouldInvokeVnodeHook = !isAsyncWrapper(vnode);
    let vnodeHook;
    if (shouldInvokeVnodeHook && (vnodeHook = props && props.onVnodeBeforeUnmount)) {
      invokeVNodeHook(vnodeHook, parentComponent, vnode);
    }
    if (shapeFlag & 6) {
      unmountComponent(vnode.component, parentSuspense, doRemove);
    } else {
      if (shapeFlag & 128) {
        vnode.suspense.unmount(parentSuspense, doRemove);
        return;
      }
      if (shouldInvokeDirs) {
        invokeDirectiveHook(vnode, null, parentComponent, "beforeUnmount");
      }
      if (shapeFlag & 64) {
        vnode.type.remove(
          vnode,
          parentComponent,
          parentSuspense,
          internals,
          doRemove
        );
      } else if (dynamicChildren && // #5154
      // when v-once is used inside a block, setBlockTracking(-1) marks the
      // parent block with hasOnce: true
      // so that it doesn't take the fast path during unmount - otherwise
      // components nested in v-once are never unmounted.
      !dynamicChildren.hasOnce && // #1153: fast path should not be taken for non-stable (v-for) fragments
      (type !== Fragment || patchFlag > 0 && patchFlag & 64)) {
        unmountChildren(
          dynamicChildren,
          parentComponent,
          parentSuspense,
          false,
          true
        );
      } else if (type === Fragment && patchFlag & (128 | 256) || !optimized && shapeFlag & 16) {
        unmountChildren(children, parentComponent, parentSuspense);
      }
      if (doRemove) {
        remove2(vnode);
      }
    }
    const shouldInvalidateMemo = memo != null && cacheIndex == null;
    if (shouldInvokeVnodeHook && (vnodeHook = props && props.onVnodeUnmounted) || shouldInvokeDirs || shouldInvalidateMemo) {
      queuePostRenderEffect(() => {
        vnodeHook && invokeVNodeHook(vnodeHook, parentComponent, vnode);
        shouldInvokeDirs && invokeDirectiveHook(vnode, null, parentComponent, "unmounted");
        if (shouldInvalidateMemo) {
          vnode.el = null;
        }
      }, parentSuspense);
    }
  };
  const remove2 = (vnode) => {
    const { type, el, anchor, transition } = vnode;
    if (type === Fragment) {
      {
        removeFragment(el, anchor);
      }
      return;
    }
    if (type === Static) {
      removeStaticNode(vnode);
      return;
    }
    const performRemove = () => {
      hostRemove(el);
      if (transition && !transition.persisted && transition.afterLeave) {
        transition.afterLeave();
      }
    };
    if (vnode.shapeFlag & 1 && transition && !transition.persisted) {
      const { leave, delayLeave } = transition;
      const performLeave = () => leave(el, performRemove);
      if (delayLeave) {
        delayLeave(vnode.el, performRemove, performLeave);
      } else {
        performLeave();
      }
    } else {
      performRemove();
    }
  };
  const removeFragment = (cur, end) => {
    let next;
    while (cur !== end) {
      next = hostNextSibling(cur);
      hostRemove(cur);
      cur = next;
    }
    hostRemove(end);
  };
  const unmountComponent = (instance, parentSuspense, doRemove) => {
    const { bum, scope, job, subTree, um, m, a } = instance;
    invalidateMount(m);
    invalidateMount(a);
    if (bum) {
      invokeArrayFns(bum);
    }
    scope.stop();
    if (job) {
      job.flags |= 8;
      unmount(subTree, instance, parentSuspense, doRemove);
    }
    if (um) {
      queuePostRenderEffect(um, parentSuspense);
    }
    queuePostRenderEffect(() => {
      instance.isUnmounted = true;
    }, parentSuspense);
  };
  const unmountChildren = (children, parentComponent, parentSuspense, doRemove = false, optimized = false, start = 0) => {
    for (let i = start; i < children.length; i++) {
      unmount(children[i], parentComponent, parentSuspense, doRemove, optimized);
    }
  };
  const getNextHostNode = (vnode) => {
    if (vnode.shapeFlag & 6) {
      return getNextHostNode(vnode.component.subTree);
    }
    if (vnode.shapeFlag & 128) {
      return vnode.suspense.next();
    }
    const el = hostNextSibling(vnode.anchor || vnode.el);
    const teleportEnd = el && el[TeleportEndKey];
    return teleportEnd ? hostNextSibling(teleportEnd) : el;
  };
  let isFlushing = false;
  const render = (vnode, container, namespace) => {
    let instance;
    if (vnode == null) {
      if (container._vnode) {
        unmount(container._vnode, null, null, true);
        instance = container._vnode.component;
      }
    } else {
      patch(
        container._vnode || null,
        vnode,
        container,
        null,
        null,
        null,
        namespace
      );
    }
    container._vnode = vnode;
    if (!isFlushing) {
      isFlushing = true;
      flushPreFlushCbs(instance);
      flushPostFlushCbs();
      isFlushing = false;
    }
  };
  const internals = {
    p: patch,
    um: unmount,
    m: move,
    r: remove2,
    mt: mountComponent,
    mc: mountChildren,
    pc: patchChildren,
    pbc: patchBlockChildren,
    n: getNextHostNode,
    o: options
  };
  let hydrate;
  return {
    render,
    hydrate,
    createApp: createAppAPI(render)
  };
}
function resolveChildrenNamespace({ type, props }, currentNamespace) {
  return currentNamespace === "svg" && type === "foreignObject" || currentNamespace === "mathml" && type === "annotation-xml" && props && props.encoding && props.encoding.includes("html") ? void 0 : currentNamespace;
}
function toggleRecurse({ effect: effect2, job }, allowed) {
  if (allowed) {
    effect2.flags |= 32;
    job.flags |= 4;
  } else {
    effect2.flags &= -33;
    job.flags &= -5;
  }
}
function needTransition(parentSuspense, transition) {
  return (!parentSuspense || parentSuspense && !parentSuspense.pendingBranch) && transition && !transition.persisted;
}
function traverseStaticChildren(n1, n2, shallow = false) {
  const ch1 = n1.children;
  const ch2 = n2.children;
  if (isArray(ch1) && isArray(ch2)) {
    for (let i = 0; i < ch1.length; i++) {
      const c1 = ch1[i];
      let c2 = ch2[i];
      if (c2.shapeFlag & 1 && !c2.dynamicChildren) {
        if (c2.patchFlag <= 0 || c2.patchFlag === 32) {
          c2 = ch2[i] = cloneIfMounted(ch2[i]);
          c2.el = c1.el;
        }
        if (!shallow && c2.patchFlag !== -2)
          traverseStaticChildren(c1, c2);
      }
      if (c2.type === Text) {
        if (c2.patchFlag === -1) {
          c2 = ch2[i] = cloneIfMounted(c2);
        }
        c2.el = c1.el;
      }
      if (c2.type === Comment && !c2.el) {
        c2.el = c1.el;
      }
    }
  }
}
function getSequence(arr) {
  const p2 = arr.slice();
  const result = [0];
  let i, j, u, v, c;
  const len = arr.length;
  for (i = 0; i < len; i++) {
    const arrI = arr[i];
    if (arrI !== 0) {
      j = result[result.length - 1];
      if (arr[j] < arrI) {
        p2[i] = j;
        result.push(i);
        continue;
      }
      u = 0;
      v = result.length - 1;
      while (u < v) {
        c = u + v >> 1;
        if (arr[result[c]] < arrI) {
          u = c + 1;
        } else {
          v = c;
        }
      }
      if (arrI < arr[result[u]]) {
        if (u > 0) {
          p2[i] = result[u - 1];
        }
        result[u] = i;
      }
    }
  }
  u = result.length;
  v = result[u - 1];
  while (u-- > 0) {
    result[u] = v;
    v = p2[v];
  }
  return result;
}
function locateNonHydratedAsyncRoot(instance) {
  const subComponent = instance.subTree.component;
  if (subComponent) {
    if (subComponent.asyncDep && !subComponent.asyncResolved) {
      return subComponent;
    } else {
      return locateNonHydratedAsyncRoot(subComponent);
    }
  }
}
function invalidateMount(hooks) {
  if (hooks) {
    for (let i = 0; i < hooks.length; i++)
      hooks[i].flags |= 8;
  }
}
function resolveAsyncComponentPlaceholder(anchorVnode) {
  if (anchorVnode.placeholder) {
    return anchorVnode.placeholder;
  }
  const instance = anchorVnode.component;
  if (instance) {
    return resolveAsyncComponentPlaceholder(instance.subTree);
  }
  return null;
}
const isSuspense = (type) => type.__isSuspense;
function queueEffectWithSuspense(fn, suspense) {
  if (suspense && suspense.pendingBranch) {
    if (isArray(fn)) {
      suspense.effects.push(...fn);
    } else {
      suspense.effects.push(fn);
    }
  } else {
    queuePostFlushCb(fn);
  }
}
const Fragment = /* @__PURE__ */ Symbol.for("v-fgt");
const Text = /* @__PURE__ */ Symbol.for("v-txt");
const Comment = /* @__PURE__ */ Symbol.for("v-cmt");
const Static = /* @__PURE__ */ Symbol.for("v-stc");
const blockStack = [];
let currentBlock = null;
function openBlock(disableTracking = false) {
  blockStack.push(currentBlock = disableTracking ? null : []);
}
function closeBlock() {
  blockStack.pop();
  currentBlock = blockStack[blockStack.length - 1] || null;
}
let isBlockTreeEnabled = 1;
function setBlockTracking(value, inVOnce = false) {
  isBlockTreeEnabled += value;
  if (value < 0 && currentBlock && inVOnce) {
    currentBlock.hasOnce = true;
  }
}
function setupBlock(vnode) {
  vnode.dynamicChildren = isBlockTreeEnabled > 0 ? currentBlock || EMPTY_ARR : null;
  closeBlock();
  if (isBlockTreeEnabled > 0 && currentBlock) {
    currentBlock.push(vnode);
  }
  return vnode;
}
function createElementBlock(type, props, children, patchFlag, dynamicProps, shapeFlag) {
  return setupBlock(
    createBaseVNode(
      type,
      props,
      children,
      patchFlag,
      dynamicProps,
      shapeFlag,
      true
    )
  );
}
function createBlock(type, props, children, patchFlag, dynamicProps) {
  return setupBlock(
    createVNode(
      type,
      props,
      children,
      patchFlag,
      dynamicProps,
      true
    )
  );
}
function isVNode(value) {
  return value ? value.__v_isVNode === true : false;
}
function isSameVNodeType(n1, n2) {
  return n1.type === n2.type && n1.key === n2.key;
}
const normalizeKey = ({ key }) => key != null ? key : null;
const normalizeRef = ({
  ref: ref3,
  ref_key,
  ref_for
}) => {
  if (typeof ref3 === "number") {
    ref3 = "" + ref3;
  }
  return ref3 != null ? isString(ref3) || /* @__PURE__ */ isRef(ref3) || isFunction(ref3) ? { i: currentRenderingInstance, r: ref3, k: ref_key, f: !!ref_for } : ref3 : null;
};
function createBaseVNode(type, props = null, children = null, patchFlag = 0, dynamicProps = null, shapeFlag = type === Fragment ? 0 : 1, isBlockNode = false, needFullChildrenNormalization = false) {
  const vnode = {
    __v_isVNode: true,
    __v_skip: true,
    type,
    props,
    key: props && normalizeKey(props),
    ref: props && normalizeRef(props),
    scopeId: currentScopeId,
    slotScopeIds: null,
    children,
    component: null,
    suspense: null,
    ssContent: null,
    ssFallback: null,
    dirs: null,
    transition: null,
    el: null,
    anchor: null,
    target: null,
    targetStart: null,
    targetAnchor: null,
    staticCount: 0,
    shapeFlag,
    patchFlag,
    dynamicProps,
    dynamicChildren: null,
    appContext: null,
    ctx: currentRenderingInstance
  };
  if (needFullChildrenNormalization) {
    normalizeChildren(vnode, children);
    if (shapeFlag & 128) {
      type.normalize(vnode);
    }
  } else if (children) {
    vnode.shapeFlag |= isString(children) ? 8 : 16;
  }
  if (isBlockTreeEnabled > 0 && // avoid a block node from tracking itself
  !isBlockNode && // has current parent block
  currentBlock && // presence of a patch flag indicates this node needs patching on updates.
  // component nodes also should always be patched, because even if the
  // component doesn't need to update, it needs to persist the instance on to
  // the next vnode so that it can be properly unmounted later.
  (vnode.patchFlag > 0 || shapeFlag & 6) && // the EVENTS flag is only for hydration and if it is the only flag, the
  // vnode should not be considered dynamic due to handler caching.
  vnode.patchFlag !== 32) {
    currentBlock.push(vnode);
  }
  return vnode;
}
const createVNode = _createVNode;
function _createVNode(type, props = null, children = null, patchFlag = 0, dynamicProps = null, isBlockNode = false) {
  if (!type || type === NULL_DYNAMIC_COMPONENT) {
    type = Comment;
  }
  if (isVNode(type)) {
    const cloned = cloneVNode(
      type,
      props,
      true
      /* mergeRef: true */
    );
    if (children) {
      normalizeChildren(cloned, children);
    }
    if (isBlockTreeEnabled > 0 && !isBlockNode && currentBlock) {
      if (cloned.shapeFlag & 6) {
        currentBlock[currentBlock.indexOf(type)] = cloned;
      } else {
        currentBlock.push(cloned);
      }
    }
    cloned.patchFlag = -2;
    return cloned;
  }
  if (isClassComponent(type)) {
    type = type.__vccOpts;
  }
  if (props) {
    props = guardReactiveProps(props);
    let { class: klass, style } = props;
    if (klass && !isString(klass)) {
      props.class = normalizeClass(klass);
    }
    if (isObject(style)) {
      if (/* @__PURE__ */ isProxy(style) && !isArray(style)) {
        style = extend({}, style);
      }
      props.style = normalizeStyle(style);
    }
  }
  const shapeFlag = isString(type) ? 1 : isSuspense(type) ? 128 : isTeleport(type) ? 64 : isObject(type) ? 4 : isFunction(type) ? 2 : 0;
  return createBaseVNode(
    type,
    props,
    children,
    patchFlag,
    dynamicProps,
    shapeFlag,
    isBlockNode,
    true
  );
}
function guardReactiveProps(props) {
  if (!props) return null;
  return /* @__PURE__ */ isProxy(props) || isInternalObject(props) ? extend({}, props) : props;
}
function cloneVNode(vnode, extraProps, mergeRef = false, cloneTransition = false) {
  const { props, ref: ref3, patchFlag, children, transition } = vnode;
  const mergedProps = extraProps ? mergeProps(props || {}, extraProps) : props;
  const cloned = {
    __v_isVNode: true,
    __v_skip: true,
    type: vnode.type,
    props: mergedProps,
    key: mergedProps && normalizeKey(mergedProps),
    ref: extraProps && extraProps.ref ? (
      // #2078 in the case of <component :is="vnode" ref="extra"/>
      // if the vnode itself already has a ref, cloneVNode will need to merge
      // the refs so the single vnode can be set on multiple refs
      mergeRef && ref3 ? isArray(ref3) ? ref3.concat(normalizeRef(extraProps)) : [ref3, normalizeRef(extraProps)] : normalizeRef(extraProps)
    ) : ref3,
    scopeId: vnode.scopeId,
    slotScopeIds: vnode.slotScopeIds,
    children,
    target: vnode.target,
    targetStart: vnode.targetStart,
    targetAnchor: vnode.targetAnchor,
    staticCount: vnode.staticCount,
    shapeFlag: vnode.shapeFlag,
    // if the vnode is cloned with extra props, we can no longer assume its
    // existing patch flag to be reliable and need to add the FULL_PROPS flag.
    // note: preserve flag for fragments since they use the flag for children
    // fast paths only.
    patchFlag: extraProps && vnode.type !== Fragment ? patchFlag === -1 ? 16 : patchFlag | 16 : patchFlag,
    dynamicProps: vnode.dynamicProps,
    dynamicChildren: vnode.dynamicChildren,
    appContext: vnode.appContext,
    dirs: vnode.dirs,
    transition,
    // These should technically only be non-null on mounted VNodes. However,
    // they *should* be copied for kept-alive vnodes. So we just always copy
    // them since them being non-null during a mount doesn't affect the logic as
    // they will simply be overwritten.
    component: vnode.component,
    suspense: vnode.suspense,
    ssContent: vnode.ssContent && cloneVNode(vnode.ssContent),
    ssFallback: vnode.ssFallback && cloneVNode(vnode.ssFallback),
    placeholder: vnode.placeholder,
    el: vnode.el,
    anchor: vnode.anchor,
    ctx: vnode.ctx,
    ce: vnode.ce
  };
  if (transition && cloneTransition) {
    setTransitionHooks(
      cloned,
      transition.clone(cloned)
    );
  }
  return cloned;
}
function createTextVNode(text = " ", flag = 0) {
  return createVNode(Text, null, text, flag);
}
function createStaticVNode(content, numberOfNodes) {
  const vnode = createVNode(Static, null, content);
  vnode.staticCount = numberOfNodes;
  return vnode;
}
function createCommentVNode(text = "", asBlock = false) {
  return asBlock ? (openBlock(), createBlock(Comment, null, text)) : createVNode(Comment, null, text);
}
function normalizeVNode(child) {
  if (child == null || typeof child === "boolean") {
    return createVNode(Comment);
  } else if (isArray(child)) {
    return createVNode(
      Fragment,
      null,
      // #3666, avoid reference pollution when reusing vnode
      child.slice()
    );
  } else if (isVNode(child)) {
    return cloneIfMounted(child);
  } else {
    return createVNode(Text, null, String(child));
  }
}
function cloneIfMounted(child) {
  return child.el === null && child.patchFlag !== -1 || child.memo ? child : cloneVNode(child);
}
function normalizeChildren(vnode, children) {
  let type = 0;
  const { shapeFlag } = vnode;
  if (children == null) {
    children = null;
  } else if (isArray(children)) {
    type = 16;
  } else if (typeof children === "object") {
    if (shapeFlag & (1 | 64)) {
      const slot = children.default;
      if (slot) {
        slot._c && (slot._d = false);
        normalizeChildren(vnode, slot());
        slot._c && (slot._d = true);
      }
      return;
    } else {
      type = 32;
      const slotFlag = children._;
      if (!slotFlag && !isInternalObject(children)) {
        children._ctx = currentRenderingInstance;
      } else if (slotFlag === 3 && currentRenderingInstance) {
        if (currentRenderingInstance.slots._ === 1) {
          children._ = 1;
        } else {
          children._ = 2;
          vnode.patchFlag |= 1024;
        }
      }
    }
  } else if (isFunction(children)) {
    if (shapeFlag & (1 | 64)) {
      normalizeChildren(vnode, { default: children });
      return;
    }
    children = { default: children, _ctx: currentRenderingInstance };
    type = 32;
  } else {
    children = String(children);
    if (shapeFlag & 64) {
      type = 16;
      children = [createTextVNode(children)];
    } else {
      type = 8;
    }
  }
  vnode.children = children;
  vnode.shapeFlag |= type;
}
function mergeProps(...args) {
  const ret = {};
  for (let i = 0; i < args.length; i++) {
    const toMerge = args[i];
    for (const key in toMerge) {
      if (key === "class") {
        if (ret.class !== toMerge.class) {
          ret.class = normalizeClass([ret.class, toMerge.class]);
        }
      } else if (key === "style") {
        ret.style = normalizeStyle([ret.style, toMerge.style]);
      } else if (isOn(key)) {
        const existing = ret[key];
        const incoming = toMerge[key];
        if (incoming && existing !== incoming && !(isArray(existing) && existing.includes(incoming))) {
          ret[key] = existing ? [].concat(existing, incoming) : incoming;
        } else if (incoming == null && existing == null && // mergeProps({ 'onUpdate:modelValue': undefined }) should not retain
        // the model listener.
        !isModelListener(key)) {
          ret[key] = incoming;
        }
      } else if (key !== "") {
        ret[key] = toMerge[key];
      }
    }
  }
  return ret;
}
function invokeVNodeHook(hook, instance, vnode, prevVNode = null) {
  callWithAsyncErrorHandling(hook, instance, 7, [
    vnode,
    prevVNode
  ]);
}
const emptyAppContext = createAppContext();
let uid = 0;
function createComponentInstance(vnode, parent, suspense) {
  const type = vnode.type;
  const appContext = (parent ? parent.appContext : vnode.appContext) || emptyAppContext;
  const instance = {
    uid: uid++,
    vnode,
    type,
    parent,
    appContext,
    root: null,
    // to be immediately set
    next: null,
    subTree: null,
    // will be set synchronously right after creation
    effect: null,
    update: null,
    // will be set synchronously right after creation
    job: null,
    scope: new EffectScope(
      true
      /* detached */
    ),
    render: null,
    proxy: null,
    exposed: null,
    exposeProxy: null,
    withProxy: null,
    provides: parent ? parent.provides : Object.create(appContext.provides),
    ids: parent ? parent.ids : ["", 0, 0],
    accessCache: null,
    renderCache: [],
    // local resolved assets
    components: null,
    directives: null,
    // resolved props and emits options
    propsOptions: normalizePropsOptions(type, appContext),
    emitsOptions: normalizeEmitsOptions(type, appContext),
    // emit
    emit: null,
    // to be set immediately
    emitted: null,
    // props default value
    propsDefaults: EMPTY_OBJ,
    // inheritAttrs
    inheritAttrs: type.inheritAttrs,
    // state
    ctx: EMPTY_OBJ,
    data: EMPTY_OBJ,
    props: EMPTY_OBJ,
    attrs: EMPTY_OBJ,
    slots: EMPTY_OBJ,
    refs: EMPTY_OBJ,
    setupState: EMPTY_OBJ,
    setupContext: null,
    // suspense related
    suspense,
    suspenseId: suspense ? suspense.pendingId : 0,
    asyncDep: null,
    asyncResolved: false,
    // lifecycle hooks
    // not using enums here because it results in computed properties
    isMounted: false,
    isUnmounted: false,
    isDeactivated: false,
    bc: null,
    c: null,
    bm: null,
    m: null,
    bu: null,
    u: null,
    um: null,
    bum: null,
    da: null,
    a: null,
    rtg: null,
    rtc: null,
    ec: null,
    sp: null
  };
  {
    instance.ctx = { _: instance };
  }
  instance.root = parent ? parent.root : instance;
  instance.emit = emit.bind(null, instance);
  if (vnode.ce) {
    vnode.ce(instance);
  }
  return instance;
}
let currentInstance = null;
const getCurrentInstance = () => currentInstance || currentRenderingInstance;
let internalSetCurrentInstance;
let setInSSRSetupState;
{
  const g = getGlobalThis();
  const registerGlobalSetter = (key, setter) => {
    let setters;
    if (!(setters = g[key])) setters = g[key] = [];
    setters.push(setter);
    return (v) => {
      if (setters.length > 1) setters.forEach((set) => set(v));
      else setters[0](v);
    };
  };
  internalSetCurrentInstance = registerGlobalSetter(
    `__VUE_INSTANCE_SETTERS__`,
    (v) => currentInstance = v
  );
  setInSSRSetupState = registerGlobalSetter(
    `__VUE_SSR_SETTERS__`,
    (v) => isInSSRComponentSetup = v
  );
}
const setCurrentInstance = (instance) => {
  const prev = currentInstance;
  internalSetCurrentInstance(instance);
  instance.scope.on();
  return () => {
    instance.scope.off();
    internalSetCurrentInstance(prev);
  };
};
const unsetCurrentInstance = () => {
  currentInstance && currentInstance.scope.off();
  internalSetCurrentInstance(null);
};
function isStatefulComponent(instance) {
  return instance.vnode.shapeFlag & 4;
}
let isInSSRComponentSetup = false;
function setupComponent(instance, isSSR = false, optimized = false) {
  isSSR && setInSSRSetupState(isSSR);
  const { props, children } = instance.vnode;
  const isStateful = isStatefulComponent(instance);
  initProps(instance, props, isStateful, isSSR);
  initSlots(instance, children, optimized || isSSR);
  const setupResult = isStateful ? setupStatefulComponent(instance, isSSR) : void 0;
  isSSR && setInSSRSetupState(false);
  return setupResult;
}
function setupStatefulComponent(instance, isSSR) {
  const Component = instance.type;
  instance.accessCache = /* @__PURE__ */ Object.create(null);
  instance.proxy = new Proxy(instance.ctx, PublicInstanceProxyHandlers);
  const { setup } = Component;
  if (setup) {
    pauseTracking();
    const setupContext = instance.setupContext = setup.length > 1 ? createSetupContext(instance) : null;
    const reset = setCurrentInstance(instance);
    const setupResult = callWithErrorHandling(
      setup,
      instance,
      0,
      [
        instance.props,
        setupContext
      ]
    );
    const isAsyncSetup = isPromise(setupResult);
    resetTracking();
    reset();
    if ((isAsyncSetup || instance.sp) && !isAsyncWrapper(instance)) {
      markAsyncBoundary(instance);
    }
    if (isAsyncSetup) {
      setupResult.then(unsetCurrentInstance, unsetCurrentInstance);
      if (isSSR) {
        return setupResult.then((resolvedResult) => {
          setInSSRSetupState(true);
          try {
            handleSetupResult(instance, resolvedResult, isSSR);
          } finally {
            setInSSRSetupState(false);
          }
        }).catch((e) => {
          handleError(e, instance, 0);
        });
      } else {
        instance.asyncDep = setupResult;
      }
    } else {
      handleSetupResult(instance, setupResult);
    }
  } else {
    finishComponentSetup(instance);
  }
}
function handleSetupResult(instance, setupResult, isSSR) {
  if (isFunction(setupResult)) {
    if (instance.type.__ssrInlineRender) {
      instance.ssrRender = setupResult;
    } else {
      instance.render = setupResult;
    }
  } else if (isObject(setupResult)) {
    instance.setupState = proxyRefs(setupResult);
  } else ;
  finishComponentSetup(instance);
}
function finishComponentSetup(instance, isSSR, skipOptions) {
  const Component = instance.type;
  if (!instance.render) {
    instance.render = Component.render || NOOP;
  }
  {
    const reset = setCurrentInstance(instance);
    pauseTracking();
    try {
      applyOptions(instance);
    } finally {
      resetTracking();
      reset();
    }
  }
}
const attrsProxyHandlers = {
  get(target, key) {
    track(target, "get", "");
    return target[key];
  }
};
function createSetupContext(instance) {
  const expose = (exposed) => {
    instance.exposed = exposed || {};
  };
  {
    return {
      attrs: new Proxy(instance.attrs, attrsProxyHandlers),
      slots: instance.slots,
      emit: instance.emit,
      expose
    };
  }
}
function getComponentPublicInstance(instance) {
  if (instance.exposed) {
    return instance.exposeProxy || (instance.exposeProxy = new Proxy(proxyRefs(markRaw(instance.exposed)), {
      get(target, key) {
        if (key in target) {
          return target[key];
        } else if (key in publicPropertiesMap) {
          return publicPropertiesMap[key](instance);
        }
      },
      has(target, key) {
        return key in target || key in publicPropertiesMap;
      }
    }));
  } else {
    return instance.proxy;
  }
}
const classifyRE = /(?:^|[-_])\w/g;
const classify = (str) => str.replace(classifyRE, (c) => c.toUpperCase()).replace(/[-_]/g, "");
function getComponentName(Component, includeInferred = true) {
  return isFunction(Component) ? Component.displayName || Component.name : Component.name || includeInferred && Component.__name;
}
function formatComponentName(instance, Component, isRoot = false) {
  let name = getComponentName(Component);
  if (!name && Component.__file) {
    const match = Component.__file.match(/([^/\\]+)\.\w+$/);
    if (match) {
      name = match[1];
    }
  }
  if (!name && instance) {
    const inferFromRegistry = (registry) => {
      for (const key in registry) {
        if (registry[key] === Component) {
          return key;
        }
      }
    };
    name = inferFromRegistry(instance.components) || instance.parent && inferFromRegistry(
      instance.parent.type.components
    ) || inferFromRegistry(instance.appContext.components);
  }
  return name ? classify(name) : isRoot ? `App` : `Anonymous`;
}
function isClassComponent(value) {
  return isFunction(value) && "__vccOpts" in value;
}
const computed = (getterOrOptions, debugOptions) => {
  const c = /* @__PURE__ */ computed$1(getterOrOptions, debugOptions, isInSSRComponentSetup);
  return c;
};
const version = "3.5.41";
/**
* @vue/runtime-dom v3.5.41
* (c) 2018-present Yuxi (Evan) You and Vue contributors
* @license MIT
**/
let policy = void 0;
const tt = typeof window !== "undefined" && window.trustedTypes;
if (tt) {
  try {
    policy = /* @__PURE__ */ tt.createPolicy("vue", {
      createHTML: (val) => val
    });
  } catch (e) {
  }
}
const unsafeToTrustedHTML = policy ? (val) => policy.createHTML(val) : (val) => val;
const svgNS = "http://www.w3.org/2000/svg";
const mathmlNS = "http://www.w3.org/1998/Math/MathML";
const doc = typeof document !== "undefined" ? document : null;
const templateContainer = doc && /* @__PURE__ */ doc.createElement("template");
const nodeOps = {
  insert: (child, parent, anchor) => {
    parent.insertBefore(child, anchor || null);
  },
  remove: (child) => {
    const parent = child.parentNode;
    if (parent) {
      parent.removeChild(child);
    }
  },
  createElement: (tag, namespace, is, props) => {
    const el = namespace === "svg" ? doc.createElementNS(svgNS, tag) : namespace === "mathml" ? doc.createElementNS(mathmlNS, tag) : is ? doc.createElement(tag, { is }) : doc.createElement(tag);
    if (tag === "select" && props && props.multiple != null) {
      el.setAttribute("multiple", props.multiple);
    }
    return el;
  },
  createText: (text) => doc.createTextNode(text),
  createComment: (text) => doc.createComment(text),
  setText: (node, text) => {
    node.nodeValue = text;
  },
  setElementText: (el, text) => {
    el.textContent = text;
  },
  parentNode: (node) => node.parentNode,
  nextSibling: (node) => node.nextSibling,
  querySelector: (selector) => doc.querySelector(selector),
  setScopeId(el, id) {
    el.setAttribute(id, "");
  },
  // __UNSAFE__
  // Reason: innerHTML.
  // Static content here can only come from compiled templates.
  // As long as the user only uses trusted templates, this is safe.
  insertStaticContent(content, parent, anchor, namespace, start, end) {
    const before = anchor ? anchor.previousSibling : parent.lastChild;
    if (start && (start === end || start.nextSibling)) {
      while (true) {
        parent.insertBefore(start.cloneNode(true), anchor);
        if (start === end || !(start = start.nextSibling)) break;
      }
    } else {
      templateContainer.innerHTML = unsafeToTrustedHTML(
        namespace === "svg" ? `<svg>${content}</svg>` : namespace === "mathml" ? `<math>${content}</math>` : content
      );
      const template = templateContainer.content;
      if (namespace === "svg" || namespace === "mathml") {
        const wrapper = template.firstChild;
        while (wrapper.firstChild) {
          template.appendChild(wrapper.firstChild);
        }
        template.removeChild(wrapper);
      }
      parent.insertBefore(template, anchor);
    }
    return [
      // first
      before ? before.nextSibling : parent.firstChild,
      // last
      anchor ? anchor.previousSibling : parent.lastChild
    ];
  }
};
const vtcKey = /* @__PURE__ */ Symbol("_vtc");
function patchClass(el, value, isSVG) {
  const transitionClasses = el[vtcKey];
  if (transitionClasses) {
    value = (value ? [value, ...transitionClasses] : [...transitionClasses]).join(" ");
  }
  if (value == null) {
    el.removeAttribute("class");
  } else if (isSVG) {
    el.setAttribute("class", value);
  } else {
    el.className = value;
  }
}
const vShowOriginalDisplay = /* @__PURE__ */ Symbol("_vod");
const vShowHidden = /* @__PURE__ */ Symbol("_vsh");
const CSS_VAR_TEXT = /* @__PURE__ */ Symbol("");
const displayRE = /(?:^|;)\s*display\s*:/;
function patchStyle(el, prev, next) {
  const style = el.style;
  const isCssString = isString(next);
  let hasControlledDisplay = false;
  if (next && !isCssString) {
    if (prev) {
      if (!isString(prev)) {
        for (const key in prev) {
          if (next[key] == null) {
            setStyle(style, key, "");
          }
        }
      } else {
        for (const prevStyle of prev.split(";")) {
          const key = prevStyle.slice(0, prevStyle.indexOf(":")).trim();
          if (next[key] == null) {
            setStyle(style, key, "");
          }
        }
      }
    }
    for (const key in next) {
      if (key === "display") {
        hasControlledDisplay = true;
      }
      const value = next[key];
      if (value != null) {
        if (!shouldPreserveTextareaResizeStyle(
          el,
          key,
          !isString(prev) && prev ? prev[key] : void 0,
          value
        )) {
          setStyle(style, key, value);
        }
      } else {
        setStyle(style, key, "");
      }
    }
  } else {
    if (isCssString) {
      if (prev !== next) {
        const cssVarText = style[CSS_VAR_TEXT];
        if (cssVarText) {
          next += ";" + cssVarText;
        }
        style.cssText = next;
        hasControlledDisplay = displayRE.test(next);
      }
    } else if (prev) {
      el.removeAttribute("style");
    }
  }
  if (vShowOriginalDisplay in el) {
    el[vShowOriginalDisplay] = hasControlledDisplay ? style.display : "";
    if (el[vShowHidden]) {
      style.display = "none";
    }
  }
}
const importantRE = /\s*!important$/;
function setStyle(style, name, val) {
  if (isArray(val)) {
    val.forEach((v) => setStyle(style, name, v));
  } else {
    if (val == null) val = "";
    if (name.startsWith("--")) {
      style.setProperty(name, val);
    } else {
      const prefixed = autoPrefix(style, name);
      if (importantRE.test(val)) {
        style.setProperty(
          hyphenate(prefixed),
          val.replace(importantRE, ""),
          "important"
        );
      } else {
        style[prefixed] = val;
      }
    }
  }
}
const prefixes = ["Webkit", "Moz", "ms"];
const prefixCache = {};
function autoPrefix(style, rawName) {
  const cached = prefixCache[rawName];
  if (cached) {
    return cached;
  }
  let name = camelize(rawName);
  if (name !== "filter" && name in style) {
    return prefixCache[rawName] = name;
  }
  name = capitalize(name);
  for (let i = 0; i < prefixes.length; i++) {
    const prefixed = prefixes[i] + name;
    if (prefixed in style) {
      return prefixCache[rawName] = prefixed;
    }
  }
  return rawName;
}
function shouldPreserveTextareaResizeStyle(el, key, prev, next) {
  return el.tagName === "TEXTAREA" && (key === "width" || key === "height") && isString(next) && prev === next;
}
const xlinkNS = "http://www.w3.org/1999/xlink";
function patchAttr(el, key, value, isSVG, instance, isBoolean = isSpecialBooleanAttr(key)) {
  if (isSVG && key.startsWith("xlink:")) {
    if (value == null) {
      el.removeAttributeNS(xlinkNS, key.slice(6, key.length));
    } else {
      el.setAttributeNS(xlinkNS, key, value);
    }
  } else {
    if (value == null || isBoolean && !includeBooleanAttr(value)) {
      el.removeAttribute(key);
    } else {
      el.setAttribute(
        key,
        isBoolean ? "" : isSymbol(value) ? String(value) : value
      );
    }
  }
}
function patchDOMProp(el, key, value, parentComponent, attrName) {
  if (key === "innerHTML" || key === "textContent") {
    if (value != null) {
      el[key] = key === "innerHTML" ? unsafeToTrustedHTML(value) : value;
    }
    return;
  }
  const tag = el.tagName;
  if (key === "value" && tag !== "PROGRESS" && // custom elements may use _value internally
  !tag.includes("-")) {
    const oldValue = tag === "OPTION" ? el.getAttribute("value") || "" : el.value;
    const newValue = value == null ? (
      // #11647: value should be set as empty string for null and undefined,
      // but <input type="checkbox"> should be set as 'on'.
      el.type === "checkbox" ? "on" : ""
    ) : String(value);
    if (oldValue !== newValue || !("_value" in el)) {
      el.value = newValue;
    }
    if (value == null) {
      el.removeAttribute(key);
    }
    el._value = value;
    return;
  }
  let needRemove = false;
  if (value === "" || value == null) {
    const type = typeof el[key];
    if (type === "boolean") {
      value = includeBooleanAttr(value);
    } else if (value == null && type === "string") {
      value = "";
      needRemove = true;
    } else if (type === "number") {
      value = 0;
      needRemove = true;
    }
  }
  try {
    el[key] = value;
  } catch (e) {
  }
  needRemove && el.removeAttribute(attrName || key);
}
function addEventListener(el, event, handler, options) {
  el.addEventListener(event, handler, options);
}
function removeEventListener(el, event, handler, options) {
  el.removeEventListener(event, handler, options);
}
const veiKey = /* @__PURE__ */ Symbol("_vei");
function patchEvent(el, rawName, prevValue, nextValue, instance = null) {
  const invokers = el[veiKey] || (el[veiKey] = {});
  const existingInvoker = invokers[rawName];
  if (nextValue && existingInvoker) {
    existingInvoker.value = nextValue;
  } else {
    const [name, options] = parseName(rawName);
    if (nextValue) {
      const invoker = invokers[rawName] = createInvoker(
        nextValue,
        instance
      );
      addEventListener(el, name, invoker, options);
    } else if (existingInvoker) {
      removeEventListener(el, name, existingInvoker, options);
      invokers[rawName] = void 0;
    }
  }
}
const optionsModifierRE = /(Once|Passive|Capture)$/;
const optionsModifierEventRE = /^on:?(?:Once|Passive|Capture)$/;
function parseName(name) {
  let options;
  let m;
  while ((m = name.match(optionsModifierRE)) && !optionsModifierEventRE.test(name)) {
    if (!options) options = {};
    name = name.slice(0, name.length - m[1].length);
    options[m[1].toLowerCase()] = true;
  }
  const event = name[2] === ":" ? name.slice(3) : hyphenate(name.slice(2));
  return [event, options];
}
let cachedNow = 0;
const p = /* @__PURE__ */ Promise.resolve();
const getNow = () => cachedNow || (p.then(() => cachedNow = 0), cachedNow = Date.now());
function createInvoker(initialValue, instance) {
  const invoker = (e) => {
    if (!e._vts) {
      e._vts = Date.now();
    } else if (e._vts <= invoker.attached) {
      return;
    }
    const value = invoker.value;
    if (isArray(value)) {
      const originalStop = e.stopImmediatePropagation;
      e.stopImmediatePropagation = () => {
        originalStop.call(e);
        e._stopped = true;
      };
      const handlers = value.slice();
      const args = [e];
      for (let i = 0; i < handlers.length; i++) {
        if (e._stopped) {
          break;
        }
        const handler = handlers[i];
        if (handler) {
          callWithAsyncErrorHandling(
            handler,
            instance,
            5,
            args
          );
        }
      }
    } else {
      callWithAsyncErrorHandling(
        value,
        instance,
        5,
        [e]
      );
    }
  };
  invoker.value = initialValue;
  invoker.attached = getNow();
  return invoker;
}
const isNativeOn = (key) => key.charCodeAt(0) === 111 && key.charCodeAt(1) === 110 && // lowercase letter
key.charCodeAt(2) > 96 && key.charCodeAt(2) < 123;
const patchProp = (el, key, prevValue, nextValue, namespace, parentComponent) => {
  const isSVG = namespace === "svg";
  if (key === "class") {
    patchClass(el, nextValue, isSVG);
  } else if (key === "style") {
    patchStyle(el, prevValue, nextValue);
  } else if (isOn(key)) {
    if (!isModelListener(key)) {
      patchEvent(el, key, prevValue, nextValue, parentComponent);
    }
  } else if (key[0] === "." ? (key = key.slice(1), true) : key[0] === "^" ? (key = key.slice(1), false) : shouldSetAsProp(el, key, nextValue, isSVG)) {
    patchDOMProp(el, key, nextValue);
    if (!el.tagName.includes("-") && (key === "value" || key === "checked" || key === "selected")) {
      patchAttr(el, key, nextValue, isSVG, parentComponent, key !== "value");
    }
  } else if (
    // #11081 force set props for possible async custom element
    el._isVueCE && // #12408 check if it's declared prop or it's async custom element
    (shouldSetAsPropForVueCE(el, key) || // @ts-expect-error _def is private
    el._def.__asyncLoader && (/[A-Z]/.test(key) || !isString(nextValue)))
  ) {
    patchDOMProp(el, camelize(key), nextValue, parentComponent, key);
  } else {
    if (key === "true-value") {
      el._trueValue = nextValue;
    } else if (key === "false-value") {
      el._falseValue = nextValue;
    }
    patchAttr(el, key, nextValue, isSVG);
  }
};
function shouldSetAsProp(el, key, value, isSVG) {
  if (isSVG) {
    if (key === "innerHTML" || key === "textContent") {
      return true;
    }
    if (key in el && isNativeOn(key) && isFunction(value)) {
      return true;
    }
    return false;
  }
  if (key === "spellcheck" || key === "draggable" || key === "translate" || key === "autocorrect") {
    return false;
  }
  if (key === "sandbox" && el.tagName === "IFRAME") {
    return false;
  }
  if (key === "form") {
    return false;
  }
  if (key === "list" && el.tagName === "INPUT") {
    return false;
  }
  if (key === "type" && el.tagName === "TEXTAREA") {
    return false;
  }
  if (key === "width" || key === "height") {
    const tag = el.tagName;
    if (tag === "IMG" || tag === "VIDEO" || tag === "CANVAS" || tag === "SOURCE") {
      return false;
    }
  }
  if (isNativeOn(key) && isString(value)) {
    return false;
  }
  return key in el;
}
function shouldSetAsPropForVueCE(el, key) {
  const props = (
    // @ts-expect-error _def is private
    el._def.props
  );
  if (!props) {
    return false;
  }
  const camelKey = camelize(key);
  return Array.isArray(props) ? props.some((prop) => camelize(prop) === camelKey) : Object.keys(props).some((prop) => camelize(prop) === camelKey);
}
const getModelAssigner = (vnode) => {
  const fn = vnode.props["onUpdate:modelValue"] || false;
  return isArray(fn) ? (value) => invokeArrayFns(fn, value) : fn;
};
function onCompositionStart(e) {
  e.target.composing = true;
}
function onCompositionEnd(e) {
  const target = e.target;
  if (target.composing) {
    target.composing = false;
    target.dispatchEvent(new Event("input"));
  }
}
const assignKey = /* @__PURE__ */ Symbol("_assign");
const initialValueKey = /* @__PURE__ */ Symbol("_initialValue");
function castValue(value, trim, number) {
  if (trim) value = value.trim();
  if (number) value = looseToNumber(value);
  return value;
}
const vModelText = {
  created(el, { modifiers: { lazy, trim, number } }, vnode) {
    if (el.parentNode) {
      if (el.type === "text") {
        el[initialValueKey] = el.defaultValue.replace(/[\r\n]/g, "");
      } else if (el.type === "textarea") {
        el[initialValueKey] = el.defaultValue.replace(/\r\n?/g, "\n");
      }
    }
    el[assignKey] = getModelAssigner(vnode);
    const castToNumber = number || vnode.props && vnode.props.type === "number";
    addEventListener(el, lazy ? "change" : "input", (e) => {
      if (e.target.composing) return;
      el[assignKey](castValue(el.value, trim, castToNumber));
    });
    if (trim || castToNumber) {
      addEventListener(el, "change", () => {
        el.value = castValue(el.value, trim, castToNumber);
      });
    }
    if (!lazy) {
      addEventListener(el, "compositionstart", onCompositionStart);
      addEventListener(el, "compositionend", onCompositionEnd);
      addEventListener(el, "change", onCompositionEnd);
    }
  },
  // set value on mounted so it's after min/max for type="range"
  mounted(el, { value, modifiers: { trim, number } }) {
    const newValue = value == null ? "" : value;
    const initialValue = el[initialValueKey];
    delete el[initialValueKey];
    if (initialValue !== void 0 && (el.type === "text" || el.type === "textarea") && el.value !== initialValue) {
      el[assignKey](castValue(el.value, trim, number));
    } else {
      el.value = newValue;
    }
  },
  beforeUpdate(el, { value, oldValue, modifiers: { lazy, trim, number } }, vnode) {
    el[assignKey] = getModelAssigner(vnode);
    if (el.composing) return;
    const elValue = (number || el.type === "number") && !/^0\d/.test(el.value) ? looseToNumber(el.value) : el.value;
    const newValue = value == null ? "" : value;
    if (elValue === newValue) {
      return;
    }
    const rootNode = el.getRootNode();
    if ((rootNode instanceof Document || rootNode instanceof ShadowRoot) && rootNode.activeElement === el && el.type !== "range") {
      if (lazy && value === oldValue) {
        return;
      }
      if (trim && el.value.trim() === newValue) {
        return;
      }
    }
    el.value = newValue;
  }
};
const vModelSelect = {
  // <select multiple> value need to be deep traversed
  deep: true,
  created(el, { value, modifiers: { number } }, vnode) {
    el._modelValue = value;
    addEventListener(el, "change", () => {
      const selectedVal = Array.prototype.filter.call(el.options, (o) => o.selected).map(
        (o) => number ? looseToNumber(getValue(o)) : getValue(o)
      );
      el[assignKey](
        el.multiple ? isSet(el._modelValue) ? new Set(selectedVal) : selectedVal : selectedVal[0]
      );
      el._assigning = true;
      nextTick(() => {
        el._assigning = false;
      });
    });
    el[assignKey] = getModelAssigner(vnode);
  },
  // set value in mounted & updated because <select> relies on its children
  // <option>s.
  mounted(el, { value }) {
    setSelected(el, value);
  },
  beforeUpdate(el, { value }, vnode) {
    el._modelValue = value;
    el[assignKey] = getModelAssigner(vnode);
  },
  updated(el, { value }) {
    if (!el._assigning) {
      setSelected(el, value);
    }
  }
};
function setSelected(el, value) {
  const isMultiple = el.multiple;
  const isArrayValue = isArray(value);
  if (isMultiple && !isArrayValue && !isSet(value)) {
    return;
  }
  for (let i = 0, l = el.options.length; i < l; i++) {
    const option = el.options[i];
    const optionValue = getValue(option);
    if (isMultiple) {
      if (isArrayValue) {
        const optionType = typeof optionValue;
        if (optionType === "string" || optionType === "number") {
          option.selected = value.some((v) => String(v) === String(optionValue));
        } else {
          option.selected = looseIndexOf(value, optionValue) > -1;
        }
      } else {
        option.selected = value.has(optionValue);
      }
    } else if (looseEqual(getValue(option), value)) {
      if (el.selectedIndex !== i) el.selectedIndex = i;
      return;
    }
  }
  if (!isMultiple && el.selectedIndex !== -1) {
    el.selectedIndex = -1;
  }
}
function getValue(el) {
  return "_value" in el ? el._value : el.value;
}
const systemModifiers = ["ctrl", "shift", "alt", "meta"];
const modifierGuards = {
  stop: (e) => e.stopPropagation(),
  prevent: (e) => e.preventDefault(),
  self: (e) => e.target !== e.currentTarget,
  ctrl: (e) => !e.ctrlKey,
  shift: (e) => !e.shiftKey,
  alt: (e) => !e.altKey,
  meta: (e) => !e.metaKey,
  left: (e) => "button" in e && e.button !== 0,
  middle: (e) => "button" in e && e.button !== 1,
  right: (e) => "button" in e && e.button !== 2,
  exact: (e, modifiers) => systemModifiers.some((m) => e[`${m}Key`] && !modifiers.includes(m))
};
const withModifiers = (fn, modifiers) => {
  if (!fn) return fn;
  const cache = fn._withMods || (fn._withMods = {});
  const cacheKey = modifiers.join(".");
  return cache[cacheKey] || (cache[cacheKey] = ((event, ...args) => {
    for (let i = 0; i < modifiers.length; i++) {
      const guard = modifierGuards[modifiers[i]];
      if (guard && guard(event, modifiers)) return;
    }
    return fn(event, ...args);
  }));
};
const rendererOptions = /* @__PURE__ */ extend({ patchProp }, nodeOps);
let renderer;
function ensureRenderer() {
  return renderer || (renderer = createRenderer(rendererOptions));
}
const createApp = ((...args) => {
  const app = ensureRenderer().createApp(...args);
  const { mount } = app;
  app.mount = (containerOrSelector) => {
    const container = normalizeContainer(containerOrSelector);
    if (!container) return;
    const component = app._component;
    if (!isFunction(component) && !component.render && !component.template) {
      component.template = container.innerHTML;
    }
    if (container.nodeType === 1) {
      container.textContent = "";
    }
    const proxy = mount(container, false, resolveRootNamespace(container));
    if (container instanceof Element) {
      container.removeAttribute("v-cloak");
      container.setAttribute("data-v-app", "");
    }
    return proxy;
  };
  return app;
});
function resolveRootNamespace(container) {
  if (container instanceof SVGElement) {
    return "svg";
  }
  if (typeof MathMLElement === "function" && container instanceof MathMLElement) {
    return "mathml";
  }
}
function normalizeContainer(container) {
  if (isString(container)) {
    const res = document.querySelector(container);
    return res;
  }
  return container;
}
const TOKEN_KEY = "strmhub_token";
function getToken() {
  return localStorage.getItem(TOKEN_KEY) || "";
}
function setToken(token) {
  if (token) localStorage.setItem(TOKEN_KEY, token);
  else localStorage.removeItem(TOKEN_KEY);
}
function isAuthed() {
  return !!getToken();
}
function normalizeError(resp, data) {
  if (data && data.detail) return String(data.detail);
  return `HTTP ${resp.status}`;
}
async function api(method, path, body) {
  const headers = {};
  const token = getToken();
  if (token) headers["Authorization"] = `Bearer ${token}`;
  if (body !== void 0) headers["Content-Type"] = "application/json";
  const resp = await fetch(path, {
    method,
    headers,
    body: body !== void 0 ? JSON.stringify(body) : void 0
  });
  const text = await resp.text();
  let data = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    data = null;
  }
  if (!resp.ok) {
    if (resp.status === 401 && !path.startsWith("/api/auth/")) {
      setToken("");
      if (typeof window !== "undefined") {
        window.dispatchEvent(new Event("strmhub-unauthorized"));
      }
      const err2 = new Error("登录已过期, 请重新登录");
      err2.status = 401;
      throw err2;
    }
    const err = new Error(normalizeError(resp, data));
    err.status = resp.status;
    throw err;
  }
  return data;
}
const http = {
  get: (p2) => api("GET", p2),
  post: (p2, b) => api("POST", p2, b),
  put: (p2, b) => api("PUT", p2, b),
  del: (p2) => api("DELETE", p2)
};
const authApi = {
  login: (password) => http.post("/api/auth/login", { password }),
  me: () => http.get("/api/me")
};
const accountApi = {
  list: () => http.get("/api/accounts"),
  create: (body) => http.post("/api/accounts", body),
  remove: (id) => http.del(`/api/accounts/${id}`),
  drivers: () => http.get("/api/accounts/drivers"),
  rules: (id) => http.get(`/api/accounts/${id}/rules`),
  saveRules: (id, rules) => http.put(`/api/accounts/${id}/rules`, { rules }),
  browse: (id, parent) => http.get(
    `/api/accounts/${id}/browse${parent ? `?parent=${encodeURIComponent(parent)}` : ""}`
  )
};
const driverRulesApi = {
  rules: (driver) => http.get(`/api/drivers/${driver}/rules`),
  save: (driver, rules) => http.put(`/api/drivers/${driver}/rules`, { rules })
};
const qrcodeApi = {
  start: (driverType) => http.post("/api/accounts/qrcode/start", { driver_type: driverType }),
  poll: (driverType, { uid: uid2, time, sign, app }) => http.post("/api/accounts/qrcode/poll", {
    driver_type: driverType,
    uid: uid2,
    time,
    sign,
    app
  })
};
const taskApi = {
  list: () => http.get("/api/tasks"),
  create: (body) => http.post("/api/tasks", body),
  remove: (id) => http.del(`/api/tasks/${id}`),
  run: (id) => http.post(`/api/tasks/${id}/run`)
};
const scrapeApi = {
  run: (strmDir) => http.post("/api/scrape/run", { strm_dir: strmDir }),
  items: (taskId) => http.get(`/api/scrape/items?task_id=${taskId}`)
};
const organizeApi = {
  plan: (path) => http.post("/api/organize/plan", { path }),
  execute: (planJson) => http.post("/api/organize/execute", { plan_json: planJson }),
  run: (accountId) => http.post("/api/organize/run", { account_id: accountId }),
  render: (template, sample) => http.post("/api/organize/render", { template, sample })
};
const automationApi = {
  list: () => http.get("/api/automation/rules"),
  create: (body) => http.post("/api/automation/rules", body),
  remove: (id) => http.del(`/api/automation/rules/${id}`)
};
const systemApi = {
  health: () => http.get("/api/health")
};
const _export_sfc = (sfc, props) => {
  const target = sfc.__vccOpts || sfc;
  for (const [key, val] of props) {
    target[key] = val;
  }
  return target;
};
const _hoisted_1$8 = { class: "login-wrap" };
const _hoisted_2$8 = {
  key: 0,
  class: "msg err"
};
const _hoisted_3$8 = ["disabled"];
const _sfc_main$8 = {
  __name: "Login",
  emits: ["login"],
  setup(__props, { emit: __emit }) {
    const emit2 = __emit;
    const password = /* @__PURE__ */ ref("");
    const error = /* @__PURE__ */ ref("");
    const busy = /* @__PURE__ */ ref(false);
    async function submit() {
      if (!password.value || busy.value) return;
      busy.value = true;
      error.value = "";
      try {
        const data = await authApi.login(password.value);
        setToken(data.token);
        emit2("login");
      } catch (e) {
        error.value = e.message;
      } finally {
        busy.value = false;
      }
    }
    return (_ctx, _cache) => {
      return openBlock(), createElementBlock("div", _hoisted_1$8, [
        createBaseVNode("form", {
          class: "card login-box",
          onSubmit: withModifiers(submit, ["prevent"])
        }, [
          _cache[1] || (_cache[1] = createBaseVNode("h1", null, "STRMhub 管理台", -1)),
          _cache[2] || (_cache[2] = createBaseVNode("p", { class: "muted" }, "请输入管理员密码(环境变量 STRMHUB_ADMIN_PASSWORD)", -1)),
          withDirectives(createBaseVNode("input", {
            "onUpdate:modelValue": _cache[0] || (_cache[0] = ($event) => password.value = $event),
            type: "password",
            placeholder: "密码",
            autofocus: ""
          }, null, 512), [
            [vModelText, password.value]
          ]),
          error.value ? (openBlock(), createElementBlock("div", _hoisted_2$8, toDisplayString(error.value), 1)) : createCommentVNode("", true),
          createBaseVNode("button", {
            class: "primary",
            type: "submit",
            disabled: busy.value
          }, toDisplayString(busy.value ? "登录中..." : "登录"), 9, _hoisted_3$8)
        ], 32)
      ]);
    };
  }
};
const Login = /* @__PURE__ */ _export_sfc(_sfc_main$8, [["__scopeId", "data-v-f4f53bcd"]]);
const _hoisted_1$7 = { class: "grid2" };
const _hoisted_2$7 = { class: "card" };
const _hoisted_3$7 = { class: "ok" };
const _hoisted_4$7 = { class: "card" };
const _hoisted_5$7 = { class: "card" };
const _sfc_main$7 = {
  __name: "Dashboard",
  setup(__props) {
    const accounts = /* @__PURE__ */ ref([]);
    const tasks = /* @__PURE__ */ ref([]);
    const health = /* @__PURE__ */ ref(null);
    const drivers = /* @__PURE__ */ ref([]);
    onMounted(async () => {
      try {
        health.value = await systemApi.health();
      } catch {
        health.value = { status: "offline" };
      }
      try {
        accounts.value = await accountApi.list();
      } catch {
      }
      try {
        tasks.value = await taskApi.list();
      } catch {
      }
      try {
        drivers.value = await accountApi.drivers();
      } catch {
      }
    });
    const running = () => tasks.value.filter((t) => t.status === "running").length;
    const done = () => tasks.value.filter((t) => t.status === "done").length;
    const failed = () => tasks.value.filter((t) => t.status === "error").length;
    return (_ctx, _cache) => {
      var _a;
      return openBlock(), createElementBlock(Fragment, null, [
        _cache[8] || (_cache[8] = createBaseVNode("h1", null, "总览", -1)),
        createBaseVNode("div", _hoisted_1$7, [
          createBaseVNode("div", _hoisted_2$7, [
            _cache[1] || (_cache[1] = createBaseVNode("h2", null, "系统", -1)),
            createBaseVNode("p", null, [
              _cache[0] || (_cache[0] = createTextVNode("后端状态: ", -1)),
              createBaseVNode("span", _hoisted_3$7, toDisplayString(((_a = health.value) == null ? void 0 : _a.status) || "未知"), 1)
            ]),
            _cache[2] || (_cache[2] = createBaseVNode("p", { class: "muted" }, "管理 API 端口 6060 · Emby 302 反代端口 6086", -1))
          ]),
          createBaseVNode("div", _hoisted_4$7, [
            _cache[5] || (_cache[5] = createBaseVNode("h2", null, "统计", -1)),
            createBaseVNode("p", null, [
              _cache[3] || (_cache[3] = createTextVNode("网盘账户: ", -1)),
              createBaseVNode("b", null, toDisplayString(accounts.value.length), 1)
            ]),
            createBaseVNode("p", null, [
              _cache[4] || (_cache[4] = createTextVNode("STRM 任务: ", -1)),
              createBaseVNode("b", null, toDisplayString(tasks.value.length), 1),
              createTextVNode(" (运行中 " + toDisplayString(running()) + " · 成功 " + toDisplayString(done()) + " · 失败 " + toDisplayString(failed()) + ")", 1)
            ])
          ])
        ]),
        createBaseVNode("div", _hoisted_5$7, [
          _cache[7] || (_cache[7] = createBaseVNode("h2", null, "可用驱动", -1)),
          createBaseVNode("table", null, [
            _cache[6] || (_cache[6] = createBaseVNode("tr", null, [
              createBaseVNode("th", null, "类型"),
              createBaseVNode("th", null, "名称"),
              createBaseVNode("th", null, "认证方式")
            ], -1)),
            (openBlock(true), createElementBlock(Fragment, null, renderList(drivers.value, (d) => {
              return openBlock(), createElementBlock("tr", {
                key: d.name
              }, [
                createBaseVNode("td", null, [
                  createBaseVNode("code", null, toDisplayString(d.name), 1)
                ]),
                createBaseVNode("td", null, toDisplayString(d.label), 1),
                createBaseVNode("td", null, toDisplayString(d.auth_type), 1)
              ]);
            }), 128))
          ])
        ])
      ], 64);
    };
  }
};
const _hoisted_1$6 = { key: 0 };
const _hoisted_2$6 = {
  class: "card",
  style: { "max-width": "640px" }
};
const _hoisted_3$6 = {
  class: "row",
  style: { "margin-bottom": "10px" }
};
const _hoisted_4$6 = ["disabled"];
const _hoisted_5$6 = {
  key: 0,
  class: "msg err",
  style: { "margin-top": "0" }
};
const _hoisted_6$6 = {
  class: "row",
  style: { "margin-bottom": "10px", "gap": "8px" }
};
const _hoisted_7$6 = {
  class: "row",
  style: { "margin-bottom": "10px", "gap": "8px" }
};
const _hoisted_8$4 = { class: "acc-head" };
const _hoisted_9$2 = ["src"];
const _hoisted_10$1 = { class: "acc-head-info" };
const _hoisted_11$1 = { class: "acc-title" };
const _hoisted_12$1 = { class: "acc-name" };
const _hoisted_13$1 = {
  key: 0,
  class: "badge ok"
};
const _hoisted_14 = {
  key: 0,
  class: "msg err",
  style: { "margin": "4px 0 0" }
};
const _hoisted_15 = {
  key: 1,
  class: "muted"
};
const _hoisted_16 = {
  key: 2,
  class: "muted"
};
const _hoisted_17 = {
  key: 3,
  class: "muted"
};
const _hoisted_18 = {
  key: 0,
  class: "acc-space"
};
const _hoisted_19 = { class: "space-row" };
const _hoisted_20 = { class: "muted" };
const _hoisted_21 = { class: "space-bar" };
const _hoisted_22 = {
  class: "row",
  style: { "margin-top": "14px" }
};
const _hoisted_23 = ["disabled"];
const _hoisted_24 = {
  key: 1,
  class: "msg err",
  style: { "margin-top": "8px" }
};
const _hoisted_25 = {
  class: "card acc-card",
  style: { "margin-top": "14px" }
};
const _hoisted_26 = { class: "acc-tabs" };
const _hoisted_27 = ["onClick"];
const _hoisted_28 = { class: "org-dir-label" };
const _hoisted_29 = ["data-tip"];
const _hoisted_30 = ["value", "disabled", "onClick"];
const _hoisted_31 = ["onClick"];
const _hoisted_32 = {
  class: "row",
  style: { "margin-top": "12px" }
};
const _hoisted_33 = ["disabled"];
const _hoisted_34 = {
  key: 0,
  class: "org-result",
  style: { "margin-top": "12px" }
};
const _hoisted_35 = { class: "row" };
const _hoisted_36 = { class: "badge ok" };
const _hoisted_37 = {
  class: "badge",
  style: { "color": "var(--warn)" }
};
const _hoisted_38 = { class: "badge err" };
const _hoisted_39 = { style: { "margin-top": "8px" } };
const _hoisted_40 = { class: "muted" };
const _hoisted_41 = { class: "muted" };
const _hoisted_42 = { class: "muted" };
const _hoisted_43 = { class: "err" };
const _hoisted_44 = {
  class: "row",
  style: { "margin-top": "12px" }
};
const _hoisted_45 = ["disabled"];
const _hoisted_46 = {
  class: "row",
  style: { "margin-bottom": "12px" }
};
const _hoisted_47 = {
  class: "muted",
  style: { "margin-top": "-6px" }
};
const _hoisted_48 = { class: "grid2" };
const _hoisted_49 = {
  class: "row",
  style: { "margin-top": "12px" }
};
const _hoisted_50 = ["disabled"];
const _hoisted_51 = {
  class: "row",
  style: { "margin-bottom": "14px" }
};
const _hoisted_52 = ["onClick"];
const _hoisted_53 = ["onUpdate:modelValue", "onInput"];
const _hoisted_54 = {
  key: 0,
  class: "rename-preview"
};
const _hoisted_55 = { class: "full-preview" };
const _hoisted_56 = { class: "tree-example" };
const _hoisted_57 = { class: "tree-example" };
const _hoisted_58 = {
  class: "row",
  style: { "margin-top": "12px" }
};
const _hoisted_59 = ["disabled"];
const _hoisted_60 = {
  class: "row",
  style: { "margin-top": "12px" }
};
const _hoisted_61 = ["disabled"];
const _hoisted_62 = { class: "pop-wrap" };
const _hoisted_63 = {
  key: 1,
  class: "pop-confirm"
};
const _hoisted_64 = { class: "pop-body" };
const _hoisted_65 = { class: "pop-foot" };
const _hoisted_66 = { class: "doc-table" };
const _hoisted_67 = { class: "muted" };
const _hoisted_68 = {
  class: "doc-table",
  style: { "margin-bottom": "12px" }
};
const _hoisted_69 = { class: "muted" };
const _hoisted_70 = {
  key: 1,
  class: "card"
};
const _hoisted_71 = { class: "acc-cell" };
const _hoisted_72 = ["src"];
const _hoisted_73 = { class: "acc-name" };
const _hoisted_74 = {
  class: "muted",
  style: { "white-space": "nowrap" }
};
const _hoisted_75 = { key: 0 };
const _hoisted_76 = {
  key: 1,
  class: "badge ok",
  style: { "margin-left": "4px" }
};
const _hoisted_77 = {
  key: 0,
  class: "muted"
};
const _hoisted_78 = {
  key: 1,
  class: "muted"
};
const _hoisted_79 = {
  key: 1,
  class: "muted"
};
const _hoisted_80 = ["onClick"];
const _hoisted_81 = { key: 0 };
const _hoisted_82 = {
  class: "modal",
  style: { "width": "420px" }
};
const _hoisted_83 = { style: { "margin-top": "0" } };
const _hoisted_84 = { class: "picker-path" };
const _hoisted_85 = ["disabled"];
const _hoisted_86 = {
  class: "muted",
  style: { "margin-left": "8px" }
};
const _hoisted_87 = {
  key: 0,
  class: "msg err"
};
const _hoisted_88 = { class: "picker-list" };
const _hoisted_89 = {
  key: 0,
  class: "muted",
  style: { "padding": "10px" }
};
const _hoisted_90 = {
  key: 1,
  class: "picker-diag"
};
const _hoisted_91 = ["onClick"];
const _hoisted_92 = { class: "modal" };
const _hoisted_93 = { style: { "text-align": "center" } };
const _hoisted_94 = ["src"];
const _hoisted_95 = { style: { "margin": "6px 0" } };
const _hoisted_96 = ["value"];
const _hoisted_97 = {
  key: 0,
  class: "warn-c"
};
const _hoisted_98 = {
  key: 1,
  class: "ok"
};
const _hoisted_99 = {
  key: 2,
  class: "ok"
};
const _hoisted_100 = {
  key: 3,
  class: "err"
};
const _hoisted_101 = {
  key: 4,
  class: "err"
};
const _hoisted_102 = {
  key: 5,
  class: "err"
};
const _hoisted_103 = {
  key: 0,
  class: "msg err",
  style: { "margin-top": "4px" }
};
const _hoisted_104 = {
  class: "row",
  style: { "justify-content": "center", "margin-top": "10px" }
};
const DICT_TEXT = `## genre_ids 内容类型 字典
#  28  Action          |  28  动作
#  12  Adventure       |  12  冒险
#  16  Animation       |  16  动画
#  35  Comedy          |  35  喜剧
#  80  Crime           |  80  犯罪
#  99  Documentary     |  99  纪录
#  18  Drama           |  18  剧情
#  10751  Family       |  10751  家庭
#  14  Fantasy         |  14  奇幻
#  36  History         |  36  历史
#  27  Horror          |  27  恐怖
#  10402  Music        |  10402  音乐
#  9648  Mystery       |  9648  悬疑
#  10749  Romance      |  10749  爱情
#  878  Science Fiction|  878  科幻
#  10770  TV Movie     |  10770  电视电影
#  53  Thriller        |  53  惊悚
#  10752  War          |  10752  战争
#  37  Western         |  37  西部

## original_language 语种 字典
#  af 南非语   ar 阿拉伯语   az 阿塞拜疆语   be 比利时语   bg 保加利亚语
#  ca 加泰隆语  cs 捷克语    cy 威尔士语    da 丹麦语     de 德语
#  dv 第维埃语  el 希腊语    en 英语       eo 世界语     es 西班牙语
#  et 爱沙尼亚语 eu 巴士克语  fa 法斯语     fi 芬兰语     fo 法罗语
#  fr 法语     gl 加里西亚语  gu 古吉拉特语  he 希伯来语   hi 印地语
#  hr 克罗地亚语 hu 匈牙利语  hy 亚美尼亚语  id 印度尼西亚语 is 冰岛语
#  it 意大利语  ja 日语      ka 格鲁吉亚语  kk 哈萨克语   kn 卡纳拉语
#  ko 朝鲜语   kok 孔卡尼语  ky 吉尔吉斯语  lt 立陶宛语   lv 拉脱维亚语
#  mi 毛利语   mk 马其顿语   mn 蒙古语     mr 马拉地语   ms 马来语
#  mt 马耳他语  nb 挪威语(伯克梅尔) nl 荷兰语   ns 北梭托语   pa 旁遮普语
#  pl 波兰语   pt 葡萄牙语   qu 克丘亚语   ro 罗马尼亚语  ru 俄语
#  sa 梵文    se 北萨摩斯语  sk 斯洛伐克语  sl 斯洛文尼亚语 sq 阿尔巴尼亚语
#  sv 瑞典语   sw 斯瓦希里语  syr 叙利亚语  ta 泰米尔语   te 泰卢固语
#  th 泰语    tl 塔加路语   tn 茨瓦纳语   tr 土耳其语   ts 宗加语
#  tt 鞑靼语   uk 乌克兰语   ur 乌都语     uz 乌兹别克语  vi 越南语
#  xh 班图语   zh 中文      cn 中文      zu 祖鲁语

## origin_country / production_countries 国家地区 字典
#  AR 阿根廷  AU 澳大利亚  BE 比利时   BR 巴西    CA 加拿大
#  CH 瑞士    CL 智利     CO 哥伦比亚  CZ 捷克    DE 德国
#  DK 丹麦    EG 埃及     ES 西班牙   FR 法国    GR 希腊
#  HK 香港    IL 以色列   IN 印度     IQ 伊拉克   IR 伊朗
#  IT 意大利   JP 日本     MM 缅甸     MO 澳门    MX 墨西哥
#  MY 马来西亚  NL 荷兰    NO 挪威     PH 菲律宾   PK 巴基斯坦
#  PL 波兰    RU 俄罗斯   SE 瑞典     SG 新加坡   TH 泰国
#  TR 土耳其   US 美国    VN 越南     CN 中国内地  GB 英国
#  TW 中国台湾  NZ 新西兰  SA 沙特阿拉伯 LA 老挝    KP 朝鲜
#  KR 韩国    PT 葡萄牙   MN 蒙古国`;
const CATEGORY_YAML_DEFAULT = `# 配置说明:
# 1. movie/tv 为固定一级分类; 二级名称即目录名, 按顺序匹配, 匹配后建立二级目录
# 2. 条件: original_language 语种 / origin_country|production_countries 地区 /
#    genre_ids 内容类型 / release_year 年份(YYYY 或 YYYY-YYYY) / TMDB 其它一级字段
# 3. 多项条件需同时满足; 一个条件多个值用逗号分隔; 前缀 ! 表示排除该值
# 4. 无任何条件的分类为兜底项(如 外语电影/未分类)

movie:
  动画电影:
    genre_ids: '16'
  华语电影:
    original_language: 'zh,cn,bo,za'
  外语电影:

tv:
  国漫:
    genre_ids: '16'
    origin_country: 'CN,TW,HK'
  日番:
    genre_ids: '16'
    origin_country: 'JP'
  纪录片:
    genre_ids: '99'
  儿童:
    genre_ids: '10762'
  综艺:
    genre_ids: '10764,10767'
  国产剧:
    origin_country: 'CN,TW,HK'
  欧美剧:
    origin_country: 'US,FR,GB,DE,ES,IT,NL,PT,RU,UK'
  日韩剧:
    origin_country: 'JP,KP,KR,TH,IN,SG'
  未分类:
`;
const _sfc_main$6 = {
  __name: "Accounts",
  props: {
    driverType: { type: String, default: "" }
  },
  setup(__props) {
    const props = __props;
    const accounts = /* @__PURE__ */ ref([]);
    const drivers = /* @__PURE__ */ ref([]);
    const msg = /* @__PURE__ */ ref("");
    const form = /* @__PURE__ */ ref({ name: "", driver_type: "", credential: "", config_json: "" });
    const qrShow = /* @__PURE__ */ ref(false);
    const qrImg = /* @__PURE__ */ ref("");
    const qrUid = /* @__PURE__ */ ref("");
    const qrTime = /* @__PURE__ */ ref("");
    const qrSign = /* @__PURE__ */ ref("");
    const qrApp = /* @__PURE__ */ ref("web");
    const qrApps = /* @__PURE__ */ ref([]);
    const qrStatus = /* @__PURE__ */ ref("");
    const qrTimer = /* @__PURE__ */ ref(null);
    const qrBusy = /* @__PURE__ */ ref(false);
    const qrError = /* @__PURE__ */ ref("");
    const isP115 = computed(() => props.driverType === "p115");
    const isCard = computed(() => !!props.driverType);
    async function load() {
      try {
        accounts.value = await accountApi.list();
        drivers.value = await accountApi.drivers();
      } catch {
      }
    }
    onMounted(load);
    watch(() => props.driverType, load);
    const filtered = computed(() => props.driverType ? accounts.value.filter((a) => a.driver_type === props.driverType) : accounts.value);
    const acct = computed(() => filtered.value[0] || {});
    const statusLabel = (s) => s === "ok" ? "正常" : s === "error" ? "异常" : s === "expired" ? "登录过期" : s || "-";
    const DRIVER_NAMES = { p115: "115 网盘", p123: "123 云盘", local: "本地文件" };
    const driverLabel = (t) => {
      var _a;
      return ((_a = drivers.value.find((d) => d.name === t)) == null ? void 0 : _a.label) || DRIVER_NAMES[t] || t;
    };
    const DEVICE_LABELS = {
      web: "网页版",
      desktop: "桌面客户端",
      ios: "苹果端",
      android: "安卓端",
      harmony: "鸿蒙端",
      alipaymini: "支付宝小程序",
      wechatmini: "微信小程序端",
      tv: "安卓电视端",
      apple_tv: "苹果电视端",
      qandroid: "115管理_安卓端",
      os_windows: "Windows端",
      os_mac: "macOS端",
      os_linux: "Linux端",
      ipad: "苹果平板端",
      qios: "115管理_苹果端",
      qipad: "115管理_平板端",
      "115ios": "115_苹果端",
      "115android": "115_安卓端",
      "115ipad": "115_平板端"
    };
    const deviceLabel = (d) => d && DEVICE_LABELS[d] ? DEVICE_LABELS[d] : d || "";
    const fmtSize = (fmt, size) => fmt || (size ? `${(size / 1024 ** 3).toFixed(1)} GB` : "");
    const usedPct = (info) => {
      const { used_size: used, total_size: total } = info || {};
      if (!used || !total) return 0;
      return Math.min(100, Math.round(used / total * 100));
    };
    const blacklistText = computed({
      get: () => (rules.value.blacklist || []).join("\n"),
      set: (v) => {
        rules.value.blacklist = v.split("\n").map((s) => s.trim()).filter(Boolean);
      }
    });
    const customWordsText = computed({
      get: () => (rules.value.custom_words || []).join("\n"),
      set: (v) => {
        rules.value.custom_words = v.split("\n").map((s) => s.trim()).filter(Boolean);
      }
    });
    const customMatchesText = computed({
      get: () => (rules.value.custom_matches || []).join("\n"),
      set: (v) => {
        rules.value.custom_matches = v.split("\n").map((s) => s.trim()).filter(Boolean);
      }
    });
    const releaseGroupsText = computed({
      get: () => (rules.value.release_groups || []).join(", "),
      set: (v) => {
        rules.value.release_groups = v.split(/[,，]/).map((s) => s.trim()).filter(Boolean);
      }
    });
    const tabs = [
      { id: "organize", label: "整理归档" },
      { id: "identify", label: "识别规则" },
      { id: "ai", label: "AI 辅助" },
      { id: "rename", label: "重命名规则" },
      { id: "category", label: "二级分类策略" },
      { id: "vars", label: "变量说明" },
      { id: "syntax", label: "语法说明" },
      { id: "dict", label: "分类字典" }
    ];
    const accTab = /* @__PURE__ */ ref(localStorage.getItem("strmhub_acctab") || "organize");
    function setTab(t) {
      accTab.value = t;
      localStorage.setItem("strmhub_acctab", t);
    }
    const VAR_DOCS = [
      ["{original_name}", "原文件名", "钢铁侠.2008.2160p.UHD.BluRay.x265.10bit.HDR.TrueHD.7.1-TnT.mkv"],
      ["{ext}", "扩展名", "iso"],
      ["{title}", "TMDB中的标题", "钢铁侠"],
      ["{en_title}", "TMDB中的英文标题 (tmdb为空时，会将中文标题转换为拼音)", "Iron Man"],
      ["{first_letter}", "标题的大写拼音首字母", "G"],
      ["{year}", "TMDB中的年份", "2008"],
      ["{tmdb_id}", "TMDB ID", "1726"],
      ["{resource_pix}", "分辨率", "2160p"],
      ["{resource_version}", "资源版本", "IMAX、HQ、3D、CC、DC"],
      ["{resource_source}", "资源来源", "USA.UHD、NF、DSNP"],
      ["{resource_type}", "资源质量", "BluRay、WEB-DL、HDTV"],
      ["{resource_effect}", "特效", "DV.HDR、DV、HDR、SDR"],
      ["{video_encode}", "视频编码", "H265.10bit、REMUX"],
      ["{audio_encode}", "音频编码", "TrueHD.7.1"],
      ["{resource_team}", "发布组", "TnT"],
      ["{fps}", "帧率", "60FPS"],
      ["{season_episode}", "季集 SxxExx", "S01E01"],
      ["{season_num}", "季号", "1"],
      ["{episode_num}", "集号", "1"],
      ["{disc_num}", "盘号", "1"],
      ["{season_name}", "季名", "东海篇"],
      ["{season_year}", "季年份 (可能为空，不建议使用)", "1999"],
      ["{episode_name}", "集名", "我是路飞！将要成为海贼王的男人！"],
      ["{custom_regex_match}", "自定义匹配", "自定义匹配"]
    ];
    const SYNTAX_DOCS = [
      ["{变量名}", "取这个变量的值"],
      ["<...>", "用尖括号包围的字符串称为 块，块里 {变量名} 表示当 {变量名}不为空时，取块里的内容。简单来说重命名规则就是写多个块，然后拼在一起"],
      ["<{{name}}...>", "给块取个名字，类似于临时变量，之后可以用 {name} 反复引用该块的值"],
      ["<?{{name}}...>", "有名字的块可以只取名不输出，便于后续引用"],
      ["{} 里支持 python 的字符串函数及语法", "见下方示例: replace/lower/upper/条件表达式等"],
      ["<{title}> 和 {title} 的区别", "<{title}> 会先判断 title 是否为空，后者是直接取 title 的值；也就是说如果你用的变量可能为空，则必须用 < > 把变量包起来"],
      ["[[ ]]", "如果想用 { }，由于和语法冲突，可以用 [[ ]] 代替，最终会替换为 { }"]
    ];
    const SYNTAX_EXAMPLES = [
      ["{resource_effect.replace('.', ' ')}", "替换 resource_effect 中的.为空格"],
      ["{resource_effect.lower()}", "将 resource_effect 转换为小写"],
      ["{resource_effect.upper()}", "将 resource_effect 转换为大写"],
      ["{'2160p' if resource_pix=='4k' else resource_pix}", "如果 resource_pix 为 4k，则返回 2160p，否则返回 resource_pix"],
      ["自定义命名规则", "自定义多个块，也是多个 <...>，最终这些块按顺序拼在一块"],
      ["文件夹命名规则示例", "{first_letter}-{title}-{year}-[tmdb={tmdb_id}]"],
      ["电影命名规则示例", "{title}.{year}<.{resource_pix}><.{fps}><.{resource_version}><.{resource_source}><.{resource_type}><.{resource_effect}><.{video_encode}><.{audio_encode}><-{resource_team}>"]
    ];
    const ORG_FIELDS = [
      { key: "pending", label: "等待整理", hint: "需要整理的影视放在这里, 开始整理后扫描此目录" },
      { key: "existing", label: "已经存在", hint: "整理完成的影视已存在时, 重复文件移动到此目录" },
      { key: "redundant", label: "冗余文件", hint: "识别失败或非影视文件移动到此目录" }
    ];
    const orgDirs = /* @__PURE__ */ ref({ pending: {}, existing: {}, redundant: {} });
    const orgMsg = /* @__PURE__ */ ref("");
    const orgBusy = /* @__PURE__ */ ref(false);
    const orgResult = /* @__PURE__ */ ref(null);
    const picker = /* @__PURE__ */ ref(null);
    const pickerBusy = /* @__PURE__ */ ref(false);
    const pickerErr = /* @__PURE__ */ ref("");
    async function openPicker(field) {
      picker.value = { field, parent: "", stack: [], dirs: [], current: { id: "", name: "根目录" } };
      await loadPickerDirs();
    }
    async function loadPickerDirs() {
      if (!picker.value) return;
      pickerBusy.value = true;
      pickerErr.value = "";
      picker.value.diagnose = null;
      try {
        const data = await accountApi.browse(acct.value.id, picker.value.parent);
        picker.value.dirs = data.dirs || [];
        picker.value.diagnose = data.diagnose || null;
      } catch (e) {
        pickerErr.value = e.message;
      } finally {
        pickerBusy.value = false;
      }
    }
    function enterDir(dir) {
      picker.value.stack.push(picker.value.current);
      picker.value.current = { id: dir.id, name: dir.name };
      picker.value.parent = dir.id;
      loadPickerDirs();
    }
    function pickerBack() {
      const prev = picker.value.stack.pop();
      picker.value.current = prev || { id: "", name: "根目录" };
      picker.value.parent = (prev == null ? void 0 : prev.id) || "";
      loadPickerDirs();
    }
    function selectThisDir() {
      orgDirs.value[picker.value.field] = { ...picker.value.current };
      picker.value = null;
      orgMsg.value = "";
    }
    function closePicker() {
      picker.value = null;
    }
    async function startOrganize() {
      var _a;
      orgMsg.value = "";
      if (!acct.value.id) {
        orgMsg.value = { type: "err", text: "请先在顶部创建/登录账户" };
        return;
      }
      if (!((_a = orgDirs.value.pending) == null ? void 0 : _a.id)) {
        orgMsg.value = { type: "err", text: "请先选择等待整理目录" };
        return;
      }
      if (!confirm("确认开始整理? 将扫描等待整理目录并移动文件")) return;
      orgBusy.value = true;
      orgResult.value = null;
      try {
        orgResult.value = await organizeApi.run(acct.value.id);
      } catch (e) {
        orgMsg.value = { type: "err", text: e.message };
      } finally {
        orgBusy.value = false;
      }
    }
    const RENAME_FIELDS = [
      { key: "movie_folder", label: "电影文件夹命名规则", sample: "movie_folder" },
      { key: "movie_file", label: "电影文件命名规则", sample: "movie_file" },
      { key: "tv_folder", label: "剧集文件夹命名规则", sample: "tv_folder" },
      { key: "season_folder", label: "季文件夹命名规则", sample: "season_folder" },
      { key: "episode_file", label: "集文件命名规则", sample: "episode_file" }
    ];
    const RENAME_DEFAULTS = {
      movie_folder: "{first_letter}-{title}-{year}-[tmdb=[[tmdb_id]]]",
      movie_file: "{title}.{year}<.{resource_pix}><.{fps}><.{resource_version}><.{resource_source}><.{resource_type}><.{resource_effect}><.{video_encode}><.{audio_encode}><-{resource_team}>",
      tv_folder: "{first_letter}-{title}-{year}-[tmdb=[[tmdb_id]]]",
      season_folder: "Season {season_num:02d}",
      episode_file: "{title}.{year}.{season_episode}<.{resource_pix}><.{fps}><.{resource_version}><.{resource_source}><.{resource_type}><.{resource_effect}><.{video_encode}><.{audio_encode}><-{resource_team}>"
    };
    const RENAME_MODES = {
      full: { label: "完整", templates: { ...RENAME_DEFAULTS } },
      normal: {
        label: "常规",
        templates: {
          movie_folder: "{title}-{year}",
          movie_file: "{title}.{year}<.{resource_pix}><.{resource_type}><-{resource_team}>",
          tv_folder: "{title}-{year}",
          season_folder: "Season {season_num:02d}",
          episode_file: "{title}.{year}.{season_episode}<.{resource_pix}>"
        }
      },
      minimal: {
        label: "精简",
        templates: {
          movie_folder: "{title}-{year}",
          movie_file: "{title}.{year}",
          tv_folder: "{title}-{year}",
          season_folder: "{season_episode}",
          episode_file: "{title}.{season_episode}"
        }
      }
    };
    const renameMode = /* @__PURE__ */ ref("full");
    const renamePreviews = /* @__PURE__ */ ref({});
    function switchRenameMode(mode) {
      renameMode.value = mode;
      const tpls = RENAME_MODES[mode].templates;
      for (const f of RENAME_FIELDS) {
        rules.value[`rename_${f.key}`] = tpls[f.key];
      }
      refreshAllPreviews();
    }
    const fullPreview = /* @__PURE__ */ ref({ movieFolder: "", movieFile: "", tvFolder: "", seasonFolder: "", episodeFile: "" });
    async function refreshFullPreview() {
      try {
        const [mf, mfl, tf, sf, efl] = await Promise.all([
          organizeApi.render(rules.value.rename_movie_folder, "movie_folder"),
          organizeApi.render(rules.value.rename_movie_file, "movie_file"),
          organizeApi.render(rules.value.rename_tv_folder, "tv_folder"),
          organizeApi.render(rules.value.rename_season_folder, "season_folder"),
          organizeApi.render(rules.value.rename_episode_file, "episode_file")
        ]);
        fullPreview.value = {
          movieFolder: mf.rendered,
          movieFile: mfl.rendered,
          tvFolder: tf.rendered,
          seasonFolder: sf.rendered,
          episodeFile: efl.rendered
        };
      } catch {
      }
    }
    let renderTimer = null;
    async function refreshPreview(key) {
      var _a;
      const sample = (_a = RENAME_FIELDS.find((f) => f.key === key)) == null ? void 0 : _a.sample;
      try {
        const data = await organizeApi.render(rules.value[`rename_${key}`], sample);
        renamePreviews.value[key] = data.rendered;
      } catch {
        renamePreviews.value[key] = "";
      }
    }
    function refreshAllPreviews() {
      for (const f of RENAME_FIELDS) refreshPreview(f.key);
      refreshFullPreview();
    }
    function onTemplateInput(key) {
      clearTimeout(renderTimer);
      renderTimer = setTimeout(() => {
        refreshPreview(key);
        refreshFullPreview();
      }, 300);
    }
    const rules = /* @__PURE__ */ ref({
      min_video_size_mb: 0,
      blacklist: [],
      // string[] 关键词
      custom_words: [],
      // "原始|替换"
      custom_matches: [],
      // "关键词|tmdb_id|movie/tv"
      release_groups: [],
      rename_template: "",
      rename_movie_folder: RENAME_DEFAULTS.movie_folder,
      rename_movie_file: RENAME_DEFAULTS.movie_file,
      rename_tv_folder: RENAME_DEFAULTS.tv_folder,
      rename_season_folder: RENAME_DEFAULTS.season_folder,
      rename_episode_file: RENAME_DEFAULTS.episode_file,
      organize_dirs: {},
      category_rules: [],
      category_yaml: "",
      ai: { enabled: false, mode: "off", api_base: "", api_key: "", model: "" }
    });
    const rulesBusy = /* @__PURE__ */ ref(false);
    const tabMsg = /* @__PURE__ */ ref({ identify: "", ai: "", rename: "", category: "", organize: "" });
    async function saveRules(key, fields) {
      if (!props.driverType) return;
      rulesBusy.value = true;
      tabMsg.value[key] = "";
      try {
        if (key === "category") rules.value.category_yaml = categoryYaml.value;
        if (key === "organize") {
          rules.value.organize_dirs = {
            pending: { ...orgDirs.value.pending },
            existing: { ...orgDirs.value.existing },
            redundant: { ...orgDirs.value.redundant }
          };
        }
        const cur = await driverRulesApi.rules(props.driverType);
        const merged = { ...cur.rules || {} };
        for (const f of fields) merged[f] = rules.value[f];
        await driverRulesApi.save(props.driverType, merged);
        tabMsg.value[key] = { type: "ok", text: "规则已保存" };
      } catch (e) {
        tabMsg.value[key] = { type: "err", text: e.message };
      } finally {
        rulesBusy.value = false;
      }
    }
    function autoGrow(e) {
      const el = e.target;
      el.style.height = "auto";
      el.style.height = `${el.scrollHeight}px`;
    }
    const categoryYaml = /* @__PURE__ */ ref("");
    const popConfirm = /* @__PURE__ */ ref(null);
    function askConfirm(title, ok) {
      popConfirm.value = { title, ok };
    }
    function doPopOk() {
      var _a;
      if ((_a = popConfirm.value) == null ? void 0 : _a.ok) popConfirm.value.ok();
      popConfirm.value = null;
    }
    function doReset() {
      categoryYaml.value = CATEGORY_YAML_DEFAULT;
      tabMsg.value.category = { type: "ok", text: "已重置为默认策略(记得保存)" };
    }
    async function loadRules() {
      var _a, _b, _c;
      if (!props.driverType) return;
      try {
        const data = await driverRulesApi.rules(props.driverType);
        const r = data.rules || {};
        rules.value = {
          min_video_size_mb: r.min_video_size_mb || 0,
          blacklist: r.blacklist || [],
          custom_words: r.custom_words || [],
          custom_matches: r.custom_matches || [],
          release_groups: r.release_groups || [],
          rename_template: r.rename_template || "",
          rename_movie_folder: r.rename_movie_folder || RENAME_DEFAULTS.movie_folder,
          rename_movie_file: r.rename_movie_file || RENAME_DEFAULTS.movie_file,
          rename_tv_folder: r.rename_tv_folder || RENAME_DEFAULTS.tv_folder,
          rename_season_folder: r.rename_season_folder || RENAME_DEFAULTS.season_folder,
          rename_episode_file: r.rename_episode_file || RENAME_DEFAULTS.episode_file,
          organize_dirs: r.organize_dirs || {},
          category_rules: r.category_rules || [],
          category_yaml: r.category_yaml || "",
          ai: { enabled: false, api_base: "", api_key: "", model: "", ...r.ai || {} }
        };
        if (!rules.value.ai.mode) {
          rules.value.ai.mode = rules.value.ai.enabled ? "assist" : "off";
        }
        categoryYaml.value = r.category_yaml || CATEGORY_YAML_DEFAULT;
        orgDirs.value = {
          pending: { ...((_a = r.organize_dirs) == null ? void 0 : _a.pending) || {} },
          existing: { ...((_b = r.organize_dirs) == null ? void 0 : _b.existing) || {} },
          redundant: { ...((_c = r.organize_dirs) == null ? void 0 : _c.redundant) || {} }
        };
      } catch {
      }
    }
    watch(() => props.driverType, () => loadRules());
    watch(accTab, (t) => {
      if (t === "rename") refreshAllPreviews();
    });
    async function create() {
      msg.value = "";
      try {
        if (!form.value.driver_type && props.driverType) form.value.driver_type = props.driverType;
        let config = {};
        if (form.value.config_json.trim()) {
          config = JSON.parse(form.value.config_json);
        }
        await accountApi.create({
          name: form.value.name,
          driver_type: form.value.driver_type,
          credential: form.value.credential,
          config
        });
        form.value = { ...form.value, name: "", credential: "", config_json: "" };
        await load();
        msg.value = { type: "ok", text: "账户已创建" };
      } catch (e) {
        msg.value = { type: "err", text: e.message };
      }
    }
    async function remove2(id) {
      if (!confirm("确认删除该账户?")) return;
      await accountApi.remove(id);
      await load();
    }
    async function startQrcode() {
      var _a, _b, _c, _d;
      if (qrBusy.value) return;
      qrBusy.value = true;
      qrError.value = "";
      msg.value = "";
      try {
        const data = await qrcodeApi.start("p115");
        qrUid.value = data.uid;
        qrTime.value = data.time;
        qrSign.value = data.sign;
        qrImg.value = data.qr_image;
        qrApps.value = data.apps || [];
        qrApp.value = ((_b = (_a = data.apps) == null ? void 0 : _a.find((a) => a.key === "web")) == null ? void 0 : _b.key) || ((_d = (_c = data.apps) == null ? void 0 : _c[0]) == null ? void 0 : _d.key) || "web";
        qrStatus.value = "waiting";
        qrShow.value = true;
        qrTimer.value = setInterval(pollQrcode, 2e3);
      } catch (e) {
        qrError.value = `扫码登录不可用: ${e.message}`;
        console.error("[STRMhub] 扫码登录失败:", e);
      } finally {
        qrBusy.value = false;
      }
    }
    async function pollQrcode() {
      var _a, _b;
      try {
        const data = await qrcodeApi.poll("p115", {
          uid: String(qrUid.value || ""),
          time: String(qrTime.value || ""),
          sign: String(qrSign.value || ""),
          app: qrApp.value || "web"
        });
        qrStatus.value = data.status;
        if (data.status === "confirmed") {
          clearInterval(qrTimer.value);
          qrTimer.value = null;
          qrShow.value = false;
          const action = ((_a = data.account) == null ? void 0 : _a.action) === "updated" ? "已更新" : "已自动创建";
          msg.value = { type: "ok", text: `扫码登录成功, 账户「${((_b = data.account) == null ? void 0 : _b.name) || ""}」${action}` };
          await load();
        } else if (data.status === "expired") {
          clearInterval(qrTimer.value);
          qrTimer.value = null;
          qrError.value = "二维码已过期, 请重新生成";
          qrStatus.value = "expired";
        } else if (data.status === "cancelled") {
          clearInterval(qrTimer.value);
          qrTimer.value = null;
          qrError.value = "已取消扫码, 请重新生成";
          qrStatus.value = "cancelled";
        }
      } catch (e) {
        if (qrTimer.value) {
          clearInterval(qrTimer.value);
          qrTimer.value = null;
        }
        qrStatus.value = "error";
        qrError.value = `轮询失败: ${e.message}`;
        console.error("[STRMhub] 扫码轮询失败:", e);
      }
    }
    function closeQrcode() {
      if (qrTimer.value) {
        clearInterval(qrTimer.value);
        qrTimer.value = null;
      }
      qrShow.value = false;
    }
    return (_ctx, _cache) => {
      var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j;
      return openBlock(), createElementBlock(Fragment, null, [
        createBaseVNode("h1", null, toDisplayString(driverLabel(props.driverType) || "网盘") + "管理", 1),
        isCard.value ? (openBlock(), createElementBlock("div", _hoisted_1$6, [
          createBaseVNode("div", _hoisted_2$6, [
            !filtered.value.length ? (openBlock(), createElementBlock(Fragment, { key: 0 }, [
              createBaseVNode("h2", null, toDisplayString(driverLabel(props.driverType)) + "账号", 1),
              isP115.value ? (openBlock(), createElementBlock(Fragment, { key: 0 }, [
                _cache[26] || (_cache[26] = createBaseVNode("p", {
                  class: "muted",
                  style: { "margin-top": "0" }
                }, "使用 115 手机 App 扫码登录, 登录后自动创建账号并获取账号信息(容量/头像/昵称等)。", -1)),
                createBaseVNode("div", _hoisted_3$6, [
                  createBaseVNode("button", {
                    class: "primary",
                    disabled: qrBusy.value,
                    onClick: startQrcode
                  }, toDisplayString(qrBusy.value ? "生成二维码中..." : "115 扫码登录"), 9, _hoisted_4$6)
                ]),
                qrError.value ? (openBlock(), createElementBlock("div", _hoisted_5$6, toDisplayString(qrError.value), 1)) : createCommentVNode("", true)
              ], 64)) : props.driverType === "p123" ? (openBlock(), createElementBlock(Fragment, { key: 1 }, [
                _cache[27] || (_cache[27] = createBaseVNode("p", {
                  class: "muted",
                  style: { "margin-top": "0" }
                }, "填写 123 云盘账号与密码(格式: 手机号:密码), 创建后自动登录。", -1)),
                createBaseVNode("div", _hoisted_6$6, [
                  withDirectives(createBaseVNode("input", {
                    "onUpdate:modelValue": _cache[0] || (_cache[0] = ($event) => form.value.name = $event),
                    placeholder: "名称(可选), 如 我的123",
                    style: { "max-width": "200px" }
                  }, null, 512), [
                    [vModelText, form.value.name]
                  ]),
                  withDirectives(createBaseVNode("input", {
                    "onUpdate:modelValue": _cache[1] || (_cache[1] = ($event) => form.value.credential = $event),
                    placeholder: "手机号:密码",
                    style: { "flex": "1" }
                  }, null, 512), [
                    [vModelText, form.value.credential]
                  ]),
                  createBaseVNode("button", {
                    class: "primary",
                    onClick: create
                  }, "创建账号")
                ]),
                msg.value ? (openBlock(), createElementBlock("div", {
                  key: 0,
                  class: normalizeClass(["msg", msg.value.type])
                }, toDisplayString(msg.value.text), 3)) : createCommentVNode("", true)
              ], 64)) : (openBlock(), createElementBlock(Fragment, { key: 2 }, [
                _cache[28] || (_cache[28] = createBaseVNode("p", {
                  class: "muted",
                  style: { "margin-top": "0" }
                }, "本地文件无需登录, 创建后即可用于 STRM 生成与整理归档。", -1)),
                createBaseVNode("div", _hoisted_7$6, [
                  withDirectives(createBaseVNode("input", {
                    "onUpdate:modelValue": _cache[2] || (_cache[2] = ($event) => form.value.name = $event),
                    placeholder: "名称, 如 我的本地文件",
                    style: { "max-width": "240px" }
                  }, null, 512), [
                    [vModelText, form.value.name]
                  ]),
                  createBaseVNode("button", {
                    class: "primary",
                    onClick: create
                  }, "创建")
                ]),
                msg.value ? (openBlock(), createElementBlock("div", {
                  key: 0,
                  class: normalizeClass(["msg", msg.value.type])
                }, toDisplayString(msg.value.text), 3)) : createCommentVNode("", true)
              ], 64))
            ], 64)) : (openBlock(), createElementBlock(Fragment, { key: 1 }, [
              createBaseVNode("div", _hoisted_8$4, [
                ((_a = acct.value.info) == null ? void 0 : _a.avatar) ? (openBlock(), createElementBlock("img", {
                  key: 0,
                  src: acct.value.info.avatar,
                  class: "acc-big-avatar",
                  alt: "头像"
                }, null, 8, _hoisted_9$2)) : createCommentVNode("", true),
                createBaseVNode("div", _hoisted_10$1, [
                  createBaseVNode("div", _hoisted_11$1, [
                    createBaseVNode("span", _hoisted_12$1, toDisplayString(acct.value.name), 1),
                    ((_b = acct.value.info) == null ? void 0 : _b.vip) ? (openBlock(), createElementBlock("span", _hoisted_13$1, toDisplayString(acct.value.info.vip), 1)) : createCommentVNode("", true),
                    createBaseVNode("span", {
                      class: normalizeClass(["badge", acct.value.status === "ok" ? "ok" : "err"])
                    }, toDisplayString(statusLabel(acct.value.status)), 3)
                  ]),
                  acct.value.status === "expired" ? (openBlock(), createElementBlock("div", _hoisted_14, "凭据已过期, 请重新登录更新状态")) : createCommentVNode("", true),
                  ((_c = acct.value.info) == null ? void 0 : _c.nickname) && acct.value.info.nickname !== acct.value.name ? (openBlock(), createElementBlock("div", _hoisted_15, "昵称: " + toDisplayString(acct.value.info.nickname), 1)) : createCommentVNode("", true),
                  ((_d = acct.value.info) == null ? void 0 : _d.device) ? (openBlock(), createElementBlock("div", _hoisted_16, "登录设备: " + toDisplayString(deviceLabel(acct.value.info.device)), 1)) : createCommentVNode("", true),
                  !acct.value.info || !Object.keys(acct.value.info).length ? (openBlock(), createElementBlock("div", _hoisted_17, "驱动: " + toDisplayString(driverLabel(props.driverType)) + "(该驱动暂不支持账号信息拉取)", 1)) : createCommentVNode("", true)
                ])
              ]),
              ((_e = acct.value.info) == null ? void 0 : _e.total_size) ? (openBlock(), createElementBlock("div", _hoisted_18, [
                createBaseVNode("div", _hoisted_19, [
                  _cache[29] || (_cache[29] = createBaseVNode("span", null, "容量", -1)),
                  createBaseVNode("span", _hoisted_20, "已用 " + toDisplayString(fmtSize(acct.value.info.used_size_fmt, acct.value.info.used_size)) + " / 总 " + toDisplayString(fmtSize(acct.value.info.total_size_fmt, acct.value.info.total_size)) + " (" + toDisplayString(usedPct(acct.value.info)) + "%)", 1)
                ]),
                createBaseVNode("div", _hoisted_21, [
                  createBaseVNode("div", {
                    class: "space-fill",
                    style: normalizeStyle({ width: usedPct(acct.value.info) + "%" })
                  }, null, 4)
                ])
              ])) : createCommentVNode("", true),
              createBaseVNode("div", _hoisted_22, [
                isP115.value ? (openBlock(), createElementBlock("button", {
                  key: 0,
                  class: "primary",
                  disabled: qrBusy.value,
                  onClick: startQrcode
                }, toDisplayString(qrBusy.value ? "生成二维码中..." : "重新扫码登录(换号)"), 9, _hoisted_23)) : props.driverType === "p123" ? (openBlock(), createElementBlock(Fragment, { key: 1 }, [
                  withDirectives(createBaseVNode("input", {
                    "onUpdate:modelValue": _cache[3] || (_cache[3] = ($event) => form.value.credential = $event),
                    placeholder: "更新凭据(手机号:密码)",
                    style: { "max-width": "260px" }
                  }, null, 512), [
                    [vModelText, form.value.credential]
                  ]),
                  createBaseVNode("button", {
                    class: "primary",
                    onClick: create
                  }, "更新凭据")
                ], 64)) : createCommentVNode("", true),
                createBaseVNode("button", {
                  class: "danger",
                  onClick: _cache[4] || (_cache[4] = ($event) => remove2(acct.value.id))
                }, "删除账户"),
                msg.value ? (openBlock(), createElementBlock("div", {
                  key: 2,
                  class: normalizeClass(["msg", msg.value.type])
                }, toDisplayString(msg.value.text), 3)) : createCommentVNode("", true)
              ]),
              qrError.value ? (openBlock(), createElementBlock("div", _hoisted_24, toDisplayString(qrError.value), 1)) : createCommentVNode("", true)
            ], 64))
          ]),
          createBaseVNode("div", _hoisted_25, [
            createBaseVNode("div", _hoisted_26, [
              (openBlock(), createElementBlock(Fragment, null, renderList(tabs, (t) => {
                return createBaseVNode("button", {
                  key: t.id,
                  class: normalizeClass({ "tab-on": accTab.value === t.id }),
                  onClick: ($event) => setTab(t.id)
                }, toDisplayString(t.label), 11, _hoisted_27);
              }), 64))
            ]),
            accTab.value === "organize" ? (openBlock(), createElementBlock(Fragment, { key: 0 }, [
              _cache[31] || (_cache[31] = createBaseVNode("h2", { style: { "margin-top": "0" } }, "整理归档", -1)),
              _cache[32] || (_cache[32] = createBaseVNode("p", {
                class: "muted",
                style: { "margin-top": "0" }
              }, '选择三个目录: 点击"选择"浏览网盘目录(无需填 cid)。开始整理后, 扫描等待整理目录并识别分类。', -1)),
              (openBlock(), createElementBlock(Fragment, null, renderList(ORG_FIELDS, (f) => {
                var _a2, _b2, _c2;
                return createBaseVNode("div", {
                  key: f.key,
                  class: "org-dir-row"
                }, [
                  createBaseVNode("div", _hoisted_28, [
                    createBaseVNode("span", null, toDisplayString(f.label), 1),
                    createBaseVNode("span", {
                      class: "help",
                      "data-tip": f.hint
                    }, "?", 8, _hoisted_29)
                  ]),
                  createBaseVNode("input", {
                    class: normalizeClass(["org-dir-value", { "muted": !((_a2 = orgDirs.value[f.key]) == null ? void 0 : _a2.id) }]),
                    readonly: "",
                    value: ((_b2 = orgDirs.value[f.key]) == null ? void 0 : _b2.name) || (acct.value.value.id ? "点击选择目录..." : "请先创建/登录账户"),
                    disabled: !acct.value.value.id,
                    onClick: ($event) => acct.value.value.id && openPicker(f.key)
                  }, null, 10, _hoisted_30),
                  ((_c2 = orgDirs.value[f.key]) == null ? void 0 : _c2.id) ? (openBlock(), createElementBlock("button", {
                    key: 0,
                    class: "danger",
                    onClick: ($event) => orgDirs.value[f.key] = {}
                  }, "清除", 8, _hoisted_31)) : createCommentVNode("", true)
                ]);
              }), 64)),
              createBaseVNode("div", _hoisted_32, [
                createBaseVNode("button", {
                  class: "primary",
                  onClick: _cache[5] || (_cache[5] = ($event) => saveRules("organize", ["organize_dirs"]))
                }, "保存目录"),
                createBaseVNode("button", {
                  class: "primary",
                  disabled: orgBusy.value,
                  onClick: startOrganize
                }, toDisplayString(orgBusy.value ? "整理中..." : "开始整理"), 9, _hoisted_33),
                tabMsg.value.organize ? (openBlock(), createElementBlock("div", {
                  key: 0,
                  class: normalizeClass(["msg", tabMsg.value.organize.type])
                }, toDisplayString(tabMsg.value.organize.text), 3)) : createCommentVNode("", true)
              ]),
              orgResult.value ? (openBlock(), createElementBlock("div", _hoisted_34, [
                createBaseVNode("div", _hoisted_35, [
                  createBaseVNode("span", _hoisted_36, "整理成功 " + toDisplayString(((_f = orgResult.value.counts) == null ? void 0 : _f.ok) || 0), 1),
                  createBaseVNode("span", _hoisted_37, "已存在 " + toDisplayString(((_g = orgResult.value.counts) == null ? void 0 : _g.existing) || 0), 1),
                  createBaseVNode("span", _hoisted_38, "冗余 " + toDisplayString(((_h = orgResult.value.counts) == null ? void 0 : _h.redundant) || 0), 1)
                ]),
                createBaseVNode("table", _hoisted_39, [
                  _cache[30] || (_cache[30] = createBaseVNode("tr", null, [
                    createBaseVNode("th", null, "文件"),
                    createBaseVNode("th", null, "结果")
                  ], -1)),
                  (openBlock(true), createElementBlock(Fragment, null, renderList(orgResult.value.ok, (it, i) => {
                    return openBlock(), createElementBlock("tr", {
                      key: "ok" + i
                    }, [
                      createBaseVNode("td", _hoisted_40, toDisplayString(it.name), 1),
                      createBaseVNode("td", null, [
                        createBaseVNode("code", null, toDisplayString(it.target), 1)
                      ])
                    ]);
                  }), 128)),
                  (openBlock(true), createElementBlock(Fragment, null, renderList(orgResult.value.existing, (it, i) => {
                    return openBlock(), createElementBlock("tr", {
                      key: "ex" + i
                    }, [
                      createBaseVNode("td", _hoisted_41, toDisplayString(it.name), 1),
                      createBaseVNode("td", null, "已存在 → " + toDisplayString(it.reason), 1)
                    ]);
                  }), 128)),
                  (openBlock(true), createElementBlock(Fragment, null, renderList(orgResult.value.redundant, (it, i) => {
                    return openBlock(), createElementBlock("tr", {
                      key: "rd" + i
                    }, [
                      createBaseVNode("td", _hoisted_42, toDisplayString(it.name), 1),
                      createBaseVNode("td", _hoisted_43, toDisplayString(it.reason), 1)
                    ]);
                  }), 128))
                ])
              ])) : createCommentVNode("", true)
            ], 64)) : accTab.value === "identify" ? (openBlock(), createElementBlock(Fragment, { key: 1 }, [
              _cache[38] || (_cache[38] = createBaseVNode("h2", { style: { "margin-top": "0" } }, "识别规则", -1)),
              createBaseVNode("div", null, [
                _cache[33] || (_cache[33] = createBaseVNode("label", null, [
                  createTextVNode("最小视频大小(MB)"),
                  createBaseVNode("span", {
                    class: "help",
                    "data-tip": "低于此大小的视频文件不纳入整理识别; 填写 0 表示不限制"
                  }, "?")
                ], -1)),
                withDirectives(createBaseVNode("input", {
                  type: "number",
                  min: "0",
                  "onUpdate:modelValue": _cache[6] || (_cache[6] = ($event) => rules.value.min_video_size_mb = $event),
                  class: "rules-input",
                  style: { "max-width": "320px" }
                }, null, 512), [
                  [
                    vModelText,
                    rules.value.min_video_size_mb,
                    void 0,
                    { number: true }
                  ]
                ])
              ]),
              createBaseVNode("div", null, [
                _cache[34] || (_cache[34] = createBaseVNode("label", null, [
                  createTextVNode("发布组扩展"),
                  createBaseVNode("span", {
                    class: "help",
                    "data-tip": "追加识别发布组; 逗号分隔多个, 如 FRDS, NEWCINE"
                  }, "?")
                ], -1)),
                withDirectives(createBaseVNode("input", {
                  "onUpdate:modelValue": _cache[7] || (_cache[7] = ($event) => releaseGroupsText.value = $event),
                  class: "rules-input",
                  placeholder: "例如: FRDS, 蓝光组, NEWCINE"
                }, null, 512), [
                  [vModelText, releaseGroupsText.value]
                ])
              ]),
              createBaseVNode("div", null, [
                _cache[35] || (_cache[35] = createBaseVNode("label", null, [
                  createTextVNode("整理黑名单"),
                  createBaseVNode("span", {
                    class: "help",
                    "data-tip": "命中关键词的文件跳过整理; 一行是一条规则, 如 trailer / sample"
                  }, "?")
                ], -1)),
                withDirectives(createBaseVNode("textarea", {
                  "onUpdate:modelValue": _cache[8] || (_cache[8] = ($event) => blacklistText.value = $event),
                  class: "rules-input",
                  rows: "4",
                  onInput: autoGrow,
                  placeholder: "例如: trailer\nsample\nxxx"
                }, null, 544), [
                  [vModelText, blacklistText.value]
                ])
              ]),
              createBaseVNode("div", null, [
                _cache[36] || (_cache[36] = createBaseVNode("label", null, [
                  createTextVNode("自定义识别词"),
                  createBaseVNode("span", {
                    class: "help",
                    "data-tip": "识别时将原始词替换为替换词; 一行是一条规则, 格式: 原始词|替换词, 如 蜘蛛侠3|Spider-Man 3"
                  }, "?")
                ], -1)),
                withDirectives(createBaseVNode("textarea", {
                  "onUpdate:modelValue": _cache[9] || (_cache[9] = ($event) => customWordsText.value = $event),
                  class: "rules-input",
                  rows: "3",
                  onInput: autoGrow,
                  placeholder: "例如: 蜘蛛侠3|Spider-Man 3\nSW|Star Wars"
                }, null, 544), [
                  [vModelText, customWordsText.value]
                ])
              ]),
              createBaseVNode("div", null, [
                _cache[37] || (_cache[37] = createBaseVNode("label", null, [
                  createTextVNode("自定义匹配"),
                  createBaseVNode("span", {
                    class: "help",
                    "data-tip": "文件名命中关键词时直接指定为对应作品; 一行是一条规则, 格式: 关键词|TMDB_ID|movie或tv"
                  }, "?")
                ], -1)),
                withDirectives(createBaseVNode("textarea", {
                  "onUpdate:modelValue": _cache[10] || (_cache[10] = ($event) => customMatchesText.value = $event),
                  class: "rules-input",
                  rows: "3",
                  onInput: autoGrow,
                  placeholder: "例如: 星际穿越|157336|movie\n三体|457433|tv"
                }, null, 544), [
                  [vModelText, customMatchesText.value]
                ])
              ]),
              createBaseVNode("div", _hoisted_44, [
                createBaseVNode("button", {
                  class: "primary",
                  disabled: rulesBusy.value,
                  onClick: _cache[11] || (_cache[11] = ($event) => saveRules("identify", ["min_video_size_mb", "blacklist", "custom_words", "custom_matches", "release_groups"]))
                }, toDisplayString(rulesBusy.value ? "保存中..." : "保存规则"), 9, _hoisted_45),
                tabMsg.value.identify ? (openBlock(), createElementBlock("div", {
                  key: 0,
                  class: normalizeClass(["msg", tabMsg.value.identify.type])
                }, toDisplayString(tabMsg.value.identify.text), 3)) : createCommentVNode("", true)
              ])
            ], 64)) : accTab.value === "ai" ? (openBlock(), createElementBlock(Fragment, { key: 2 }, [
              _cache[42] || (_cache[42] = createBaseVNode("h2", { style: { "margin-top": "0" } }, "AI 辅助识别", -1)),
              _cache[43] || (_cache[43] = createBaseVNode("p", {
                class: "muted",
                style: { "margin-top": "0" }
              }, "使用大模型辅助识别文件名(OpenAI 兼容接口)。", -1)),
              createBaseVNode("div", _hoisted_46, [
                createBaseVNode("button", {
                  class: normalizeClass(["ai-mode", { on: rules.value.ai.mode === "off" }]),
                  onClick: _cache[12] || (_cache[12] = ($event) => rules.value.ai.mode = "off")
                }, "禁用", 2),
                createBaseVNode("button", {
                  class: normalizeClass(["ai-mode", { on: rules.value.ai.mode === "assist" }]),
                  onClick: _cache[13] || (_cache[13] = ($event) => rules.value.ai.mode = "assist")
                }, "辅助识别", 2),
                createBaseVNode("button", {
                  class: normalizeClass(["ai-mode", { on: rules.value.ai.mode === "force" }]),
                  onClick: _cache[14] || (_cache[14] = ($event) => rules.value.ai.mode = "force")
                }, "强制识别", 2)
              ]),
              createBaseVNode("p", _hoisted_47, [
                rules.value.ai.mode === "off" ? (openBlock(), createElementBlock(Fragment, { key: 0 }, [
                  createTextVNode("不使用 AI, 仅用内置识别规则。")
                ], 64)) : rules.value.ai.mode === "assist" ? (openBlock(), createElementBlock(Fragment, { key: 1 }, [
                  createTextVNode("内置识别结果不准确时, 使用 AI 辅助识别文件名。")
                ], 64)) : (openBlock(), createElementBlock(Fragment, { key: 2 }, [
                  createTextVNode("不使用内置识别规则, 直接由 AI 识别文件名或目录名。")
                ], 64))
              ]),
              createBaseVNode("div", _hoisted_48, [
                createBaseVNode("div", null, [
                  _cache[39] || (_cache[39] = createBaseVNode("label", null, "API Base", -1)),
                  withDirectives(createBaseVNode("input", {
                    "onUpdate:modelValue": _cache[15] || (_cache[15] = ($event) => rules.value.ai.api_base = $event),
                    placeholder: "https://api.openai.com/v1"
                  }, null, 512), [
                    [vModelText, rules.value.ai.api_base]
                  ])
                ]),
                createBaseVNode("div", null, [
                  _cache[40] || (_cache[40] = createBaseVNode("label", null, "模型", -1)),
                  withDirectives(createBaseVNode("input", {
                    "onUpdate:modelValue": _cache[16] || (_cache[16] = ($event) => rules.value.ai.model = $event),
                    placeholder: "gpt-4o-mini / deepseek-chat"
                  }, null, 512), [
                    [vModelText, rules.value.ai.model]
                  ])
                ])
              ]),
              createBaseVNode("div", null, [
                _cache[41] || (_cache[41] = createBaseVNode("label", null, "API Key", -1)),
                withDirectives(createBaseVNode("input", {
                  "onUpdate:modelValue": _cache[17] || (_cache[17] = ($event) => rules.value.ai.api_key = $event),
                  type: "password",
                  placeholder: "sk-..."
                }, null, 512), [
                  [vModelText, rules.value.ai.api_key]
                ])
              ]),
              createBaseVNode("div", _hoisted_49, [
                createBaseVNode("button", {
                  class: "primary",
                  disabled: rulesBusy.value,
                  onClick: _cache[18] || (_cache[18] = ($event) => saveRules("ai", ["ai"]))
                }, toDisplayString(rulesBusy.value ? "保存中..." : "保存规则"), 9, _hoisted_50),
                tabMsg.value.ai ? (openBlock(), createElementBlock("div", {
                  key: 0,
                  class: normalizeClass(["msg", tabMsg.value.ai.type])
                }, toDisplayString(tabMsg.value.ai.text), 3)) : createCommentVNode("", true)
              ])
            ], 64)) : accTab.value === "rename" ? (openBlock(), createElementBlock(Fragment, { key: 3 }, [
              _cache[50] || (_cache[50] = createBaseVNode("h2", { style: { "margin-top": "0" } }, "重命名规则", -1)),
              createBaseVNode("div", _hoisted_51, [
                _cache[44] || (_cache[44] = createBaseVNode("span", {
                  class: "muted",
                  style: { "margin-right": "6px" }
                }, "模板方式:", -1)),
                (openBlock(), createElementBlock(Fragment, null, renderList(RENAME_MODES, (m, key) => {
                  return createBaseVNode("button", {
                    key,
                    class: normalizeClass(["ai-mode", { on: renameMode.value === key }]),
                    onClick: ($event) => switchRenameMode(key)
                  }, toDisplayString(m.label), 11, _hoisted_52);
                }), 64)),
                _cache[45] || (_cache[45] = createBaseVNode("span", {
                  class: "help",
                  "data-tip": "完整: 全变量模板(推荐); 常规: 常用变量; 精简: 最简文件名; 切换后仍可手动修改任意模板"
                }, "?", -1))
              ]),
              (openBlock(), createElementBlock(Fragment, null, renderList(RENAME_FIELDS, (f) => {
                return createBaseVNode("div", {
                  key: f.key,
                  style: { "margin-bottom": "14px" }
                }, [
                  createBaseVNode("label", null, toDisplayString(f.label), 1),
                  withDirectives(createBaseVNode("input", {
                    "onUpdate:modelValue": ($event) => rules.value[`rename_${f.key}`] = $event,
                    onInput: ($event) => onTemplateInput(f.key)
                  }, null, 40, _hoisted_53), [
                    [vModelText, rules.value[`rename_${f.key}`]]
                  ]),
                  renamePreviews.value[f.key] ? (openBlock(), createElementBlock("div", _hoisted_54, [
                    _cache[46] || (_cache[46] = createTextVNode(" 示例: ", -1)),
                    createBaseVNode("code", null, toDisplayString(renamePreviews.value[f.key]), 1)
                  ])) : createCommentVNode("", true)
                ]);
              }), 64)),
              createBaseVNode("div", _hoisted_55, [
                _cache[47] || (_cache[47] = createBaseVNode("h3", { style: { "font-size": "14px", "margin": "10px 0 6px" } }, "🎬 电影完整示例(目录结构)", -1)),
                createBaseVNode("pre", _hoisted_56, "📁 " + toDisplayString(fullPreview.value.movieFolder || "...") + "/\n   └ " + toDisplayString(fullPreview.value.movieFile || "..."), 1),
                _cache[48] || (_cache[48] = createBaseVNode("h3", { style: { "font-size": "14px", "margin": "10px 0 6px" } }, "📺 剧集完整示例(目录结构)", -1)),
                createBaseVNode("pre", _hoisted_57, "📁 " + toDisplayString(fullPreview.value.tvFolder || "...") + "/\n   📁 " + toDisplayString(fullPreview.value.seasonFolder || "...") + "/\n      └ " + toDisplayString(fullPreview.value.episodeFile || "..."), 1),
                _cache[49] || (_cache[49] = createBaseVNode("p", {
                  class: "muted",
                  style: { "margin-top": "8px" }
                }, [
                  createTextVNode('变量与语法详见"变量说明"/"语法说明" tab('),
                  createBaseVNode("code", null, "<...>"),
                  createTextVNode(" 块 / "),
                  createBaseVNode("code", null, "[[ ]]"),
                  createTextVNode(" 转义)。")
                ], -1))
              ]),
              createBaseVNode("div", _hoisted_58, [
                createBaseVNode("button", {
                  class: "primary",
                  disabled: rulesBusy.value,
                  onClick: _cache[19] || (_cache[19] = ($event) => saveRules("rename", ["rename_movie_folder", "rename_movie_file", "rename_tv_folder", "rename_season_folder", "rename_episode_file"]))
                }, toDisplayString(rulesBusy.value ? "保存中..." : "保存规则"), 9, _hoisted_59),
                tabMsg.value.rename ? (openBlock(), createElementBlock("div", {
                  key: 0,
                  class: normalizeClass(["msg", tabMsg.value.rename.type])
                }, toDisplayString(tabMsg.value.rename.text), 3)) : createCommentVNode("", true)
              ])
            ], 64)) : accTab.value === "category" ? (openBlock(), createElementBlock(Fragment, { key: 4 }, [
              _cache[52] || (_cache[52] = createBaseVNode("h2", { style: { "margin-top": "0" } }, "二级分类策略", -1)),
              _cache[53] || (_cache[53] = createBaseVNode("p", {
                class: "muted",
                style: { "margin-top": "0" }
              }, [
                createTextVNode("YAML 方式配置(优先级从上到下): 分类名 → 目标目录 cid(115 用 cid, 123 用 cid123); 整理时按 TMDB 类型/地区匹配分类并"),
                createBaseVNode("strong", null, "自动创建目录结构"),
                createTextVNode("。")
              ], -1)),
              withDirectives(createBaseVNode("textarea", {
                "onUpdate:modelValue": _cache[20] || (_cache[20] = ($event) => categoryYaml.value = $event),
                rows: "18",
                class: "yaml-editor",
                placeholder: "movie:\n  动画电影:\n    genre_ids: '16'\n  ...\ntv:\n  ..."
              }, null, 512), [
                [vModelText, categoryYaml.value]
              ]),
              _cache[54] || (_cache[54] = createStaticVNode('<p class="muted" style="margin-top:6px;">字段: <code>cid</code> 115 目标目录 · <code>cid123</code> 123 目标目录 · <code>genre_ids</code> 类型 · <code>origin_country</code>/<code>production_countries</code> 地区 · <code>original_language</code> 语种 · <code>release_year</code> 年份(支持 YYYY-YYYY); 多值逗号分隔, <code>!值</code> 排除; 无条件的分类为兜底项。</p>', 1)),
              createBaseVNode("div", _hoisted_60, [
                createBaseVNode("button", {
                  class: "primary",
                  disabled: rulesBusy.value,
                  onClick: _cache[21] || (_cache[21] = ($event) => saveRules("category", ["category_yaml"]))
                }, toDisplayString(rulesBusy.value ? "保存中..." : "保存策略"), 9, _hoisted_61),
                createBaseVNode("span", _hoisted_62, [
                  createBaseVNode("button", {
                    onClick: _cache[22] || (_cache[22] = ($event) => askConfirm("确认要重置所有配置吗？", doReset))
                  }, "重置策略"),
                  popConfirm.value ? (openBlock(), createElementBlock("div", {
                    key: 0,
                    class: "pop-mask",
                    onClick: _cache[23] || (_cache[23] = ($event) => popConfirm.value = null)
                  })) : createCommentVNode("", true),
                  popConfirm.value ? (openBlock(), createElementBlock("div", _hoisted_63, [
                    createBaseVNode("div", _hoisted_64, [
                      _cache[51] || (_cache[51] = createBaseVNode("span", { class: "pop-icon" }, [
                        createBaseVNode("svg", {
                          viewBox: "0 0 48 48",
                          width: "18",
                          height: "18",
                          fill: "none",
                          xmlns: "http://www.w3.org/2000/svg",
                          stroke: "currentColor",
                          "stroke-width": "4",
                          "stroke-linecap": "butt",
                          "stroke-linejoin": "miter"
                        }, [
                          createBaseVNode("path", {
                            "fill-rule": "evenodd",
                            "clip-rule": "evenodd",
                            d: "M24 44c11.046 0 20-8.954 20-20S35.046 4 24 4 4 12.954 4 24s8.954 20 20 20Zm-2-11a1 1 0 0 0 1 1h2a1 1 0 0 0 1-1v-2a1 1 0 0 0-1-1h-2a1 1 0 0 0-1 1v2Zm4-18a1 1 0 0 0-1-1h-2a1 1 0 0 0-1 1v12a1 1 0 0 0 1 1h2a1 1 0 0 0 1-1V15Z",
                            fill: "currentColor",
                            stroke: "none"
                          })
                        ])
                      ], -1)),
                      createBaseVNode("span", null, toDisplayString(popConfirm.value.title), 1)
                    ]),
                    createBaseVNode("div", _hoisted_65, [
                      createBaseVNode("button", {
                        class: "pop-btn",
                        onClick: _cache[24] || (_cache[24] = ($event) => popConfirm.value = null)
                      }, "取消"),
                      createBaseVNode("button", {
                        class: "pop-btn pop-btn-primary",
                        onClick: doPopOk
                      }, "确定")
                    ])
                  ])) : createCommentVNode("", true)
                ]),
                tabMsg.value.category ? (openBlock(), createElementBlock("div", {
                  key: 0,
                  class: normalizeClass(["msg", tabMsg.value.category.type])
                }, toDisplayString(tabMsg.value.category.text), 3)) : createCommentVNode("", true)
              ])
            ], 64)) : accTab.value === "vars" ? (openBlock(), createElementBlock(Fragment, { key: 5 }, [
              _cache[56] || (_cache[56] = createBaseVNode("h2", { style: { "margin-top": "0" } }, "变量说明", -1)),
              _cache[57] || (_cache[57] = createBaseVNode("p", {
                class: "muted",
                style: { "margin-top": "0" }
              }, "重命名规则中可用的变量(识别结果)。", -1)),
              createBaseVNode("table", _hoisted_66, [
                _cache[55] || (_cache[55] = createBaseVNode("tr", null, [
                  createBaseVNode("th", null, "变量名"),
                  createBaseVNode("th", null, "说明"),
                  createBaseVNode("th", null, "示例值")
                ], -1)),
                (openBlock(), createElementBlock(Fragment, null, renderList(VAR_DOCS, (row, i) => {
                  return createBaseVNode("tr", { key: i }, [
                    createBaseVNode("td", null, [
                      createBaseVNode("code", null, toDisplayString(row[0]), 1)
                    ]),
                    createBaseVNode("td", null, toDisplayString(row[1]), 1),
                    createBaseVNode("td", _hoisted_67, toDisplayString(row[2]), 1)
                  ]);
                }), 64))
              ])
            ], 64)) : accTab.value === "dict" ? (openBlock(), createElementBlock(Fragment, { key: 6 }, [
              _cache[58] || (_cache[58] = createBaseVNode("h2", { style: { "margin-top": "0" } }, "分类字典", -1)),
              _cache[59] || (_cache[59] = createBaseVNode("p", {
                class: "muted",
                style: { "margin-top": "0" }
              }, "二级分类策略中使用的类型/语种/国家地区代码速查表。", -1)),
              createBaseVNode("pre", { class: "dict-pre" }, toDisplayString(DICT_TEXT))
            ], 64)) : accTab.value === "syntax" ? (openBlock(), createElementBlock(Fragment, { key: 7 }, [
              _cache[62] || (_cache[62] = createBaseVNode("h2", { style: { "margin-top": "0" } }, "语法说明", -1)),
              createBaseVNode("table", _hoisted_68, [
                _cache[60] || (_cache[60] = createBaseVNode("tr", null, [
                  createBaseVNode("th", null, "语法"),
                  createBaseVNode("th", null, "说明")
                ], -1)),
                (openBlock(), createElementBlock(Fragment, null, renderList(SYNTAX_DOCS, (row, i) => {
                  return createBaseVNode("tr", { key: i }, [
                    createBaseVNode("td", null, [
                      createBaseVNode("code", null, toDisplayString(row[0]), 1)
                    ]),
                    createBaseVNode("td", null, toDisplayString(row[1]), 1)
                  ]);
                }), 64))
              ]),
              _cache[63] || (_cache[63] = createBaseVNode("h3", { style: { "font-size": "14px", "margin": "10px 0 6px" } }, "示例", -1)),
              (openBlock(), createElementBlock(Fragment, null, renderList(SYNTAX_EXAMPLES, (ex, i) => {
                return createBaseVNode("p", {
                  key: i,
                  class: "doc-example"
                }, [
                  createBaseVNode("code", null, toDisplayString(ex[0]), 1),
                  _cache[61] || (_cache[61] = createBaseVNode("br", null, null, -1)),
                  createBaseVNode("span", _hoisted_69, toDisplayString(ex[1]), 1)
                ]);
              }), 64))
            ], 64)) : createCommentVNode("", true)
          ])
        ])) : createCommentVNode("", true),
        !isCard.value ? (openBlock(), createElementBlock("div", _hoisted_70, [
          createBaseVNode("h2", null, toDisplayString(props.driverType ? `${driverLabel(props.driverType)}账户列表` : "账户列表") + "(凭据已加密存储)", 1),
          createBaseVNode("table", null, [
            _cache[65] || (_cache[65] = createBaseVNode("tr", null, [
              createBaseVNode("th", null, "ID"),
              createBaseVNode("th", null, "账号"),
              createBaseVNode("th", null, "驱动"),
              createBaseVNode("th", null, "信息"),
              createBaseVNode("th", null, "状态"),
              createBaseVNode("th", null, "操作")
            ], -1)),
            (openBlock(true), createElementBlock(Fragment, null, renderList(filtered.value, (a) => {
              var _a2;
              return openBlock(), createElementBlock("tr", {
                key: a.id
              }, [
                createBaseVNode("td", null, toDisplayString(a.id), 1),
                createBaseVNode("td", null, [
                  createBaseVNode("div", _hoisted_71, [
                    ((_a2 = a.info) == null ? void 0 : _a2.avatar) ? (openBlock(), createElementBlock("img", {
                      key: 0,
                      src: a.info.avatar,
                      class: "acc-avatar",
                      alt: "头像"
                    }, null, 8, _hoisted_72)) : createCommentVNode("", true),
                    createBaseVNode("span", _hoisted_73, toDisplayString(a.name), 1)
                  ])
                ]),
                createBaseVNode("td", null, [
                  createBaseVNode("code", null, toDisplayString(a.driver_type), 1)
                ]),
                createBaseVNode("td", null, [
                  a.info && Object.keys(a.info).length ? (openBlock(), createElementBlock(Fragment, { key: 0 }, [
                    createBaseVNode("div", _hoisted_74, [
                      a.info.nickname ? (openBlock(), createElementBlock("span", _hoisted_75, "昵称: " + toDisplayString(a.info.nickname), 1)) : createCommentVNode("", true),
                      a.info.vip ? (openBlock(), createElementBlock("span", _hoisted_76, toDisplayString(a.info.vip), 1)) : createCommentVNode("", true)
                    ]),
                    a.info.used_size_fmt || a.info.total_size_fmt ? (openBlock(), createElementBlock("div", _hoisted_77, " 容量: " + toDisplayString(fmtSize(a.info.used_size_fmt, a.info.used_size)) + " / " + toDisplayString(fmtSize(a.info.total_size_fmt, a.info.total_size)), 1)) : createCommentVNode("", true),
                    a.info.device ? (openBlock(), createElementBlock("div", _hoisted_78, "登录设备: " + toDisplayString(deviceLabel(a.info.device)), 1)) : createCommentVNode("", true)
                  ], 64)) : (openBlock(), createElementBlock("span", _hoisted_79, "-"))
                ]),
                createBaseVNode("td", null, [
                  createBaseVNode("span", {
                    class: normalizeClass(["badge", a.status === "ok" ? "ok" : "err"])
                  }, toDisplayString(statusLabel(a.status)), 3)
                ]),
                createBaseVNode("td", null, [
                  createBaseVNode("button", {
                    class: "danger",
                    onClick: ($event) => remove2(a.id)
                  }, "删除", 8, _hoisted_80)
                ])
              ]);
            }), 128)),
            !filtered.value.length ? (openBlock(), createElementBlock("tr", _hoisted_81, [..._cache[64] || (_cache[64] = [
              createBaseVNode("td", {
                colspan: "6",
                class: "muted"
              }, "暂无账户", -1)
            ])])) : createCommentVNode("", true)
          ])
        ])) : createCommentVNode("", true),
        picker.value ? (openBlock(), createElementBlock("div", {
          key: 2,
          class: "modal-mask",
          onClick: withModifiers(closePicker, ["self"])
        }, [
          createBaseVNode("div", _hoisted_82, [
            createBaseVNode("h2", _hoisted_83, "选择目录 — " + toDisplayString((_i = ORG_FIELDS.find((f) => f.key === picker.value.field)) == null ? void 0 : _i.label), 1),
            createBaseVNode("div", _hoisted_84, [
              createBaseVNode("button", {
                onClick: pickerBack,
                disabled: !picker.value.stack.length
              }, "← 返回", 8, _hoisted_85),
              createBaseVNode("span", _hoisted_86, toDisplayString(picker.value.current.name || "根目录"), 1)
            ]),
            pickerErr.value ? (openBlock(), createElementBlock("div", _hoisted_87, toDisplayString(pickerErr.value), 1)) : createCommentVNode("", true),
            createBaseVNode("div", _hoisted_88, [
              !pickerBusy.value && !picker.value.dirs.length ? (openBlock(), createElementBlock("div", _hoisted_89, "无子目录")) : createCommentVNode("", true),
              !pickerBusy.value && picker.value.diagnose && picker.value.diagnose.rows > 0 ? (openBlock(), createElementBlock("div", _hoisted_90, [
                createTextVNode(" 诊断(rows=" + toDisplayString(picker.value.diagnose.rows) + ", 文件=" + toDisplayString(((_j = picker.value.diagnose.all_files) == null ? void 0 : _j.length) || 0) + "): ", 1),
                createBaseVNode("pre", null, toDisplayString(JSON.stringify(picker.value.diagnose, null, 1)), 1)
              ])) : createCommentVNode("", true),
              (openBlock(true), createElementBlock(Fragment, null, renderList(picker.value.dirs, (d) => {
                return openBlock(), createElementBlock("button", {
                  key: d.id,
                  class: "picker-dir",
                  onClick: ($event) => enterDir(d)
                }, "📁 " + toDisplayString(d.name), 9, _hoisted_91);
              }), 128))
            ]),
            createBaseVNode("div", {
              class: "row",
              style: { "justify-content": "space-between", "margin-top": "10px" }
            }, [
              createBaseVNode("button", {
                class: "primary",
                onClick: selectThisDir
              }, "选择当前目录"),
              createBaseVNode("button", { onClick: closePicker }, "取消")
            ])
          ])
        ])) : createCommentVNode("", true),
        qrShow.value ? (openBlock(), createElementBlock("div", {
          key: 3,
          class: "modal-mask",
          onClick: withModifiers(closeQrcode, ["self"])
        }, [
          createBaseVNode("div", _hoisted_92, [
            _cache[68] || (_cache[68] = createBaseVNode("h2", { style: { "margin-top": "0" } }, "115 扫码登录", -1)),
            createBaseVNode("div", _hoisted_93, [
              createBaseVNode("img", {
                src: qrImg.value,
                alt: "二维码",
                style: { "width": "220px", "height": "220px", "border": "1px solid var(--line)", "border-radius": "8px" }
              }, null, 8, _hoisted_94),
              _cache[67] || (_cache[67] = createBaseVNode("p", { class: "muted" }, "打开 115 手机 App → 扫一扫 登录", -1)),
              createBaseVNode("p", _hoisted_95, [
                _cache[66] || (_cache[66] = createBaseVNode("label", { for: "qr-app" }, "登录设备:", -1)),
                withDirectives(createBaseVNode("select", {
                  id: "qr-app",
                  "onUpdate:modelValue": _cache[25] || (_cache[25] = ($event) => qrApp.value = $event),
                  style: { "margin-left": "8px" }
                }, [
                  (openBlock(true), createElementBlock(Fragment, null, renderList(qrApps.value, (a) => {
                    return openBlock(), createElementBlock("option", {
                      key: a.key,
                      value: a.key
                    }, toDisplayString(a.label), 9, _hoisted_96);
                  }), 128))
                ], 512), [
                  [vModelSelect, qrApp.value]
                ])
              ]),
              createBaseVNode("p", null, [
                qrStatus.value === "waiting" ? (openBlock(), createElementBlock("span", _hoisted_97, "等待扫码...")) : qrStatus.value === "scanned" ? (openBlock(), createElementBlock("span", _hoisted_98, "已扫码, 请在手机上确认")) : qrStatus.value === "confirmed" ? (openBlock(), createElementBlock("span", _hoisted_99, "登录成功")) : qrStatus.value === "expired" ? (openBlock(), createElementBlock("span", _hoisted_100, "二维码已过期, 请重新生成")) : qrStatus.value === "cancelled" ? (openBlock(), createElementBlock("span", _hoisted_101, "已取消扫码")) : qrStatus.value === "error" ? (openBlock(), createElementBlock("span", _hoisted_102, "轮询失败(网络/服务异常)")) : createCommentVNode("", true)
              ]),
              qrError.value ? (openBlock(), createElementBlock("p", _hoisted_103, toDisplayString(qrError.value), 1)) : createCommentVNode("", true),
              createBaseVNode("div", _hoisted_104, [
                createBaseVNode("button", { onClick: closeQrcode }, "关闭"),
                qrStatus.value === "expired" || qrStatus.value === "error" || qrStatus.value === "cancelled" ? (openBlock(), createElementBlock("button", {
                  key: 0,
                  class: "primary",
                  style: { "margin-left": "8px" },
                  onClick: startQrcode
                }, "重新生成")) : createCommentVNode("", true)
              ])
            ])
          ])
        ])) : createCommentVNode("", true)
      ], 64);
    };
  }
};
const _hoisted_1$5 = { class: "card" };
const _hoisted_2$5 = { class: "grid2" };
const _hoisted_3$5 = ["value"];
const _hoisted_4$5 = ["value"];
const _hoisted_5$5 = {
  class: "row",
  style: { "margin-top": "10px" }
};
const _hoisted_6$5 = { class: "card" };
const _hoisted_7$5 = { class: "muted" };
const _hoisted_8$3 = {
  key: 0,
  class: "err"
};
const _hoisted_9$1 = { class: "row" };
const _hoisted_10 = ["disabled", "onClick"];
const _hoisted_11 = ["onClick"];
const _hoisted_12 = { key: 0 };
const _hoisted_13 = {
  key: 0,
  class: "card"
};
const _sfc_main$5 = {
  __name: "Tasks",
  setup(__props) {
    const tasks = /* @__PURE__ */ ref([]);
    const accounts = /* @__PURE__ */ ref([]);
    const form = /* @__PURE__ */ ref({
      name: "",
      account_id: null,
      remote_path: "",
      local_output: "",
      scan_mode: "incremental_missing",
      extensions: "",
      base_url: "",
      token: ""
    });
    const msg = /* @__PURE__ */ ref("");
    const runningId = /* @__PURE__ */ ref(null);
    const lastResult = /* @__PURE__ */ ref(null);
    const scanModes = [
      ["incremental_missing", "增量补缺(只补缺少的 strm)"],
      ["incremental_update", "增量更新(内容不同才重写)"],
      ["full_sync", "全量覆写"]
    ];
    async function load() {
      try {
        tasks.value = await taskApi.list();
        accounts.value = await accountApi.list();
      } catch {
      }
    }
    onMounted(load);
    const accountName = (id) => {
      var _a;
      return ((_a = accounts.value.find((a) => a.id === id)) == null ? void 0 : _a.name) || id;
    };
    async function create() {
      msg.value = "";
      try {
        const extensions = form.value.extensions ? form.value.extensions.split(",").map((s) => s.trim()).filter(Boolean) : [];
        await taskApi.create({
          account_id: Number(form.value.account_id),
          name: form.value.name,
          remote_path: form.value.remote_path,
          local_output: form.value.local_output,
          scan_mode: form.value.scan_mode,
          extensions,
          base_url: form.value.base_url,
          token: form.value.token
        });
        form.value = { ...form.value, name: "", extensions: "" };
        await load();
        msg.value = { type: "ok", text: "任务已创建" };
      } catch (e) {
        msg.value = { type: "err", text: e.message };
      }
    }
    async function run(task) {
      runningId.value = task.id;
      lastResult.value = null;
      try {
        lastResult.value = await taskApi.run(task.id);
        await load();
      } catch (e) {
        msg.value = { type: "err", text: e.message };
      } finally {
        runningId.value = null;
      }
    }
    async function remove2(id) {
      if (!confirm("确认删除任务?")) return;
      await taskApi.remove(id);
      await load();
    }
    const statusClass = (s) => ({ running: "run", done: "ok", error: "err" })[s] || "";
    return (_ctx, _cache) => {
      return openBlock(), createElementBlock(Fragment, null, [
        _cache[22] || (_cache[22] = createBaseVNode("h1", null, "STRM 任务", -1)),
        createBaseVNode("div", _hoisted_1$5, [
          _cache[17] || (_cache[17] = createBaseVNode("h2", null, "新建任务", -1)),
          createBaseVNode("div", _hoisted_2$5, [
            createBaseVNode("div", null, [
              _cache[8] || (_cache[8] = createBaseVNode("label", null, "任务名称", -1)),
              withDirectives(createBaseVNode("input", {
                "onUpdate:modelValue": _cache[0] || (_cache[0] = ($event) => form.value.name = $event),
                placeholder: "如: 115 电影库"
              }, null, 512), [
                [vModelText, form.value.name]
              ])
            ]),
            createBaseVNode("div", null, [
              _cache[10] || (_cache[10] = createBaseVNode("label", null, "网盘账户", -1)),
              withDirectives(createBaseVNode("select", {
                "onUpdate:modelValue": _cache[1] || (_cache[1] = ($event) => form.value.account_id = $event)
              }, [
                _cache[9] || (_cache[9] = createBaseVNode("option", {
                  value: null,
                  disabled: ""
                }, "选择账户", -1)),
                (openBlock(true), createElementBlock(Fragment, null, renderList(accounts.value, (a) => {
                  return openBlock(), createElementBlock("option", {
                    key: a.id,
                    value: a.id
                  }, toDisplayString(a.name), 9, _hoisted_3$5);
                }), 128))
              ], 512), [
                [vModelSelect, form.value.account_id]
              ])
            ]),
            createBaseVNode("div", null, [
              _cache[11] || (_cache[11] = createBaseVNode("label", null, "源目录(远端路径)", -1)),
              withDirectives(createBaseVNode("input", {
                "onUpdate:modelValue": _cache[2] || (_cache[2] = ($event) => form.value.remote_path = $event),
                placeholder: "115 目录 ID / 本地路径"
              }, null, 512), [
                [vModelText, form.value.remote_path]
              ])
            ]),
            createBaseVNode("div", null, [
              _cache[12] || (_cache[12] = createBaseVNode("label", null, "STRM 输出目录", -1)),
              withDirectives(createBaseVNode("input", {
                "onUpdate:modelValue": _cache[3] || (_cache[3] = ($event) => form.value.local_output = $event),
                placeholder: "如 /strm/media"
              }, null, 512), [
                [vModelText, form.value.local_output]
              ])
            ]),
            createBaseVNode("div", null, [
              _cache[13] || (_cache[13] = createBaseVNode("label", null, "扫描模式", -1)),
              withDirectives(createBaseVNode("select", {
                "onUpdate:modelValue": _cache[4] || (_cache[4] = ($event) => form.value.scan_mode = $event)
              }, [
                (openBlock(), createElementBlock(Fragment, null, renderList(scanModes, ([v, l]) => {
                  return createBaseVNode("option", {
                    key: v,
                    value: v
                  }, toDisplayString(l), 9, _hoisted_4$5);
                }), 64))
              ], 512), [
                [vModelSelect, form.value.scan_mode]
              ])
            ]),
            createBaseVNode("div", null, [
              _cache[14] || (_cache[14] = createBaseVNode("label", null, "扩展名(逗号分隔, 留空=默认媒体集)", -1)),
              withDirectives(createBaseVNode("input", {
                "onUpdate:modelValue": _cache[5] || (_cache[5] = ($event) => form.value.extensions = $event),
                placeholder: "mkv,mp4"
              }, null, 512), [
                [vModelText, form.value.extensions]
              ])
            ]),
            createBaseVNode("div", null, [
              _cache[15] || (_cache[15] = createBaseVNode("label", null, "base_url(302 端点地址)", -1)),
              withDirectives(createBaseVNode("input", {
                "onUpdate:modelValue": _cache[6] || (_cache[6] = ($event) => form.value.base_url = $event),
                placeholder: "http://hub:6060"
              }, null, 512), [
                [vModelText, form.value.base_url]
              ])
            ]),
            createBaseVNode("div", null, [
              _cache[16] || (_cache[16] = createBaseVNode("label", null, "token(留空自动生成)", -1)),
              withDirectives(createBaseVNode("input", {
                "onUpdate:modelValue": _cache[7] || (_cache[7] = ($event) => form.value.token = $event)
              }, null, 512), [
                [vModelText, form.value.token]
              ])
            ])
          ]),
          createBaseVNode("div", _hoisted_5$5, [
            createBaseVNode("button", {
              class: "primary",
              onClick: create
            }, "创建"),
            msg.value ? (openBlock(), createElementBlock("div", {
              key: 0,
              class: normalizeClass(["msg", msg.value.type])
            }, toDisplayString(msg.value.text), 3)) : createCommentVNode("", true)
          ])
        ]),
        createBaseVNode("div", _hoisted_6$5, [
          _cache[21] || (_cache[21] = createBaseVNode("h2", null, "任务列表", -1)),
          createBaseVNode("table", null, [
            _cache[19] || (_cache[19] = createBaseVNode("tr", null, [
              createBaseVNode("th", null, "ID"),
              createBaseVNode("th", null, "名称"),
              createBaseVNode("th", null, "账户"),
              createBaseVNode("th", null, "模式"),
              createBaseVNode("th", null, "状态"),
              createBaseVNode("th", null, "最近运行"),
              createBaseVNode("th", null, "操作")
            ], -1)),
            (openBlock(true), createElementBlock(Fragment, null, renderList(tasks.value, (t) => {
              return openBlock(), createElementBlock("tr", {
                key: t.id
              }, [
                createBaseVNode("td", null, toDisplayString(t.id), 1),
                createBaseVNode("td", null, toDisplayString(t.name), 1),
                createBaseVNode("td", null, toDisplayString(accountName(t.account_id)), 1),
                createBaseVNode("td", null, [
                  createBaseVNode("code", null, toDisplayString(t.scan_mode), 1)
                ]),
                createBaseVNode("td", null, [
                  createBaseVNode("span", {
                    class: normalizeClass(["badge", statusClass(t.status)])
                  }, toDisplayString(t.status), 3)
                ]),
                createBaseVNode("td", _hoisted_7$5, [
                  createTextVNode(toDisplayString(t.last_run_at || "从未") + " ", 1),
                  t.last_error ? (openBlock(), createElementBlock("div", _hoisted_8$3, toDisplayString(t.last_error), 1)) : createCommentVNode("", true)
                ]),
                createBaseVNode("td", null, [
                  createBaseVNode("div", _hoisted_9$1, [
                    createBaseVNode("button", {
                      class: "primary",
                      disabled: runningId.value === t.id,
                      onClick: ($event) => run(t)
                    }, toDisplayString(runningId.value === t.id ? "运行中..." : "运行"), 9, _hoisted_10),
                    createBaseVNode("button", {
                      class: "danger",
                      onClick: ($event) => remove2(t.id)
                    }, "删除", 8, _hoisted_11)
                  ])
                ])
              ]);
            }), 128)),
            !tasks.value.length ? (openBlock(), createElementBlock("tr", _hoisted_12, [..._cache[18] || (_cache[18] = [
              createBaseVNode("td", {
                colspan: "7",
                class: "muted"
              }, "暂无任务", -1)
            ])])) : createCommentVNode("", true)
          ]),
          lastResult.value ? (openBlock(), createElementBlock("div", _hoisted_13, [
            _cache[20] || (_cache[20] = createBaseVNode("h2", null, "运行结果", -1)),
            createBaseVNode("pre", null, toDisplayString(JSON.stringify(lastResult.value, null, 2)), 1)
          ])) : createCommentVNode("", true)
        ])
      ], 64);
    };
  }
};
const _hoisted_1$4 = { class: "card" };
const _hoisted_2$4 = { class: "grid2" };
const _hoisted_3$4 = { class: "row" };
const _hoisted_4$4 = {
  class: "row",
  style: { "margin-top": "10px" }
};
const _hoisted_5$4 = {
  key: 0,
  class: "muted",
  style: { "margin-top": "8px" }
};
const _hoisted_6$4 = { class: "card" };
const _hoisted_7$4 = { key: 0 };
const _sfc_main$4 = {
  __name: "Scrape",
  setup(__props) {
    const strmDir = /* @__PURE__ */ ref("");
    const taskId = /* @__PURE__ */ ref("");
    const result = /* @__PURE__ */ ref(null);
    const items = /* @__PURE__ */ ref([]);
    const msg = /* @__PURE__ */ ref("");
    async function run() {
      msg.value = "";
      try {
        result.value = await scrapeApi.run(strmDir.value);
        taskId.value = result.value.task_id;
        await loadItems();
      } catch (e) {
        msg.value = { type: "err", text: e.message };
      }
    }
    async function loadItems() {
      if (!taskId.value) return;
      items.value = await scrapeApi.items(taskId.value);
    }
    onMounted(async () => {
      const saved = localStorage.getItem("strmhub_scrape_task");
      if (saved) {
        taskId.value = saved;
        await loadItems();
      }
    });
    async function loadSaved() {
      if (!taskId.value) return;
      localStorage.setItem("strmhub_scrape_task", taskId.value);
      await loadItems();
    }
    const statusText = (s) => ({ matched: "已匹配", doubt: "存疑", none: "未匹配" })[s] || s;
    return (_ctx, _cache) => {
      return openBlock(), createElementBlock(Fragment, null, [
        _cache[8] || (_cache[8] = createBaseVNode("h1", null, "刮削与海报墙", -1)),
        createBaseVNode("div", _hoisted_1$4, [
          _cache[4] || (_cache[4] = createBaseVNode("h2", null, "刮削 STRM 目录(需 TMDB_API_KEY)", -1)),
          createBaseVNode("div", _hoisted_2$4, [
            createBaseVNode("div", null, [
              _cache[2] || (_cache[2] = createBaseVNode("label", null, "STRM 目录", -1)),
              withDirectives(createBaseVNode("input", {
                "onUpdate:modelValue": _cache[0] || (_cache[0] = ($event) => strmDir.value = $event),
                placeholder: "如 /strm/media"
              }, null, 512), [
                [vModelText, strmDir.value]
              ])
            ]),
            createBaseVNode("div", null, [
              _cache[3] || (_cache[3] = createBaseVNode("label", null, "查询已有任务(海报墙)", -1)),
              createBaseVNode("div", _hoisted_3$4, [
                withDirectives(createBaseVNode("input", {
                  "onUpdate:modelValue": _cache[1] || (_cache[1] = ($event) => taskId.value = $event),
                  placeholder: "task_id"
                }, null, 512), [
                  [vModelText, taskId.value]
                ]),
                createBaseVNode("button", { onClick: loadSaved }, "查询")
              ])
            ])
          ]),
          createBaseVNode("div", _hoisted_4$4, [
            createBaseVNode("button", {
              class: "primary",
              onClick: run
            }, "开始刮削"),
            msg.value ? (openBlock(), createElementBlock("div", {
              key: 0,
              class: normalizeClass(["msg", msg.value.type])
            }, toDisplayString(msg.value.text), 3)) : createCommentVNode("", true)
          ]),
          result.value ? (openBlock(), createElementBlock("div", _hoisted_5$4, " 作品组 " + toDisplayString(result.value.groups) + " · 匹配 " + toDisplayString(result.value.matched) + " · 存疑 " + toDisplayString(result.value.doubt) + " · 未匹配 " + toDisplayString(result.value.none) + " · 海报 " + toDisplayString(result.value.posters), 1)) : createCommentVNode("", true)
        ]),
        createBaseVNode("div", _hoisted_6$4, [
          _cache[7] || (_cache[7] = createBaseVNode("h2", null, "海报墙(追更状态)", -1)),
          createBaseVNode("table", null, [
            _cache[6] || (_cache[6] = createBaseVNode("tr", null, [
              createBaseVNode("th", null, "标题"),
              createBaseVNode("th", null, "年份"),
              createBaseVNode("th", null, "类型"),
              createBaseVNode("th", null, "状态"),
              createBaseVNode("th", null, "TMDB ID"),
              createBaseVNode("th", null, "集数(本地/TMDB)")
            ], -1)),
            (openBlock(true), createElementBlock(Fragment, null, renderList(items.value, (it) => {
              return openBlock(), createElementBlock("tr", {
                key: it.title
              }, [
                createBaseVNode("td", null, toDisplayString(it.title), 1),
                createBaseVNode("td", null, toDisplayString(it.year || "-"), 1),
                createBaseVNode("td", null, toDisplayString(it.media_type === "tv" ? "剧集" : "电影"), 1),
                createBaseVNode("td", null, [
                  createBaseVNode("span", {
                    class: normalizeClass(["badge", { ok: it.status === "matched", "warn-c": it.status === "doubt", err: it.status === "none" }])
                  }, toDisplayString(statusText(it.status)), 3)
                ]),
                createBaseVNode("td", null, toDisplayString(it.tmdb_id || "-"), 1),
                createBaseVNode("td", null, toDisplayString(it.ep_local) + "/" + toDisplayString(it.ep_tmdb || "?"), 1)
              ]);
            }), 128)),
            !items.value.length ? (openBlock(), createElementBlock("tr", _hoisted_7$4, [..._cache[5] || (_cache[5] = [
              createBaseVNode("td", {
                colspan: "6",
                class: "muted"
              }, "暂无条目", -1)
            ])])) : createCommentVNode("", true)
          ])
        ])
      ], 64);
    };
  }
};
const _hoisted_1$3 = { class: "card" };
const _hoisted_2$3 = { class: "row" };
const _hoisted_3$3 = {
  key: 0,
  class: "card"
};
const _hoisted_4$3 = { class: "muted" };
const _hoisted_5$3 = { class: "muted" };
const _hoisted_6$3 = { key: 0 };
const _hoisted_7$3 = {
  class: "row",
  style: { "margin-top": "10px" }
};
const _hoisted_8$2 = ["disabled"];
const _sfc_main$3 = {
  __name: "Organize",
  setup(__props) {
    const path = /* @__PURE__ */ ref("");
    const plan = /* @__PURE__ */ ref(null);
    const msg = /* @__PURE__ */ ref("");
    async function makePlan() {
      msg.value = "";
      plan.value = null;
      try {
        plan.value = await organizeApi.plan(path.value);
      } catch (e) {
        msg.value = { type: "err", text: e.message };
      }
    }
    async function execute() {
      if (!plan.value) return;
      if (!confirm(`确认执行 ${plan.value.preview.length} 条重命名?`)) return;
      try {
        const res = await organizeApi.execute(plan.value.plan_json);
        msg.value = { type: "ok", text: `执行完成: 成功 ${res.done}, 跳过 ${res.skipped}` };
        plan.value = null;
      } catch (e) {
        msg.value = { type: "err", text: e.message };
      }
    }
    return (_ctx, _cache) => {
      return openBlock(), createElementBlock(Fragment, null, [
        _cache[4] || (_cache[4] = createBaseVNode("h1", null, "目录整理(计划-预览-执行)", -1)),
        createBaseVNode("div", _hoisted_1$3, [
          createBaseVNode("div", null, [
            _cache[1] || (_cache[1] = createBaseVNode("label", null, "扫描目录", -1)),
            createBaseVNode("div", _hoisted_2$3, [
              withDirectives(createBaseVNode("input", {
                "onUpdate:modelValue": _cache[0] || (_cache[0] = ($event) => path.value = $event),
                placeholder: "如 /strm/media"
              }, null, 512), [
                [vModelText, path.value]
              ]),
              createBaseVNode("button", {
                class: "primary",
                onClick: makePlan
              }, "生成计划")
            ])
          ]),
          msg.value ? (openBlock(), createElementBlock("div", {
            key: 0,
            class: normalizeClass(["msg", msg.value.type])
          }, toDisplayString(msg.value.text), 3)) : createCommentVNode("", true)
        ]),
        plan.value ? (openBlock(), createElementBlock("div", _hoisted_3$3, [
          createBaseVNode("h2", null, [
            createTextVNode("计划预览(" + toDisplayString(plan.value.preview.length) + " 条) ", 1),
            createBaseVNode("span", _hoisted_4$3, "plan_id: " + toDisplayString(plan.value.plan_id), 1)
          ]),
          createBaseVNode("table", null, [
            _cache[3] || (_cache[3] = createBaseVNode("tr", null, [
              createBaseVNode("th", null, "源文件"),
              createBaseVNode("th", null, "目标文件")
            ], -1)),
            (openBlock(true), createElementBlock(Fragment, null, renderList(plan.value.preview, (a, i) => {
              return openBlock(), createElementBlock("tr", { key: i }, [
                createBaseVNode("td", _hoisted_5$3, toDisplayString(a.source), 1),
                createBaseVNode("td", null, toDisplayString(a.target), 1)
              ]);
            }), 128)),
            !plan.value.preview.length ? (openBlock(), createElementBlock("tr", _hoisted_6$3, [..._cache[2] || (_cache[2] = [
              createBaseVNode("td", {
                colspan: "2",
                class: "muted"
              }, "无需整理(文件名已规范)", -1)
            ])])) : createCommentVNode("", true)
          ]),
          createBaseVNode("div", _hoisted_7$3, [
            createBaseVNode("button", {
              class: "primary",
              disabled: !plan.value.preview.length,
              onClick: execute
            }, "执行重命名", 8, _hoisted_8$2)
          ])
        ])) : createCommentVNode("", true)
      ], 64);
    };
  }
};
const _hoisted_1$2 = { class: "log-panel log-page" };
const _hoisted_2$2 = { class: "log-head" };
const _hoisted_3$2 = {
  key: 0,
  class: "log-errbar"
};
const _hoisted_4$2 = { class: "log-ts" };
const _hoisted_5$2 = { class: "log-lv" };
const _hoisted_6$2 = { class: "log-msg" };
const _hoisted_7$2 = {
  key: 0,
  class: "muted",
  style: { "padding": "10px" }
};
const _sfc_main$2 = {
  __name: "Logs",
  setup(__props) {
    const logLines = /* @__PURE__ */ ref([]);
    const logPaused = /* @__PURE__ */ ref(false);
    const logAutoScroll = /* @__PURE__ */ ref(true);
    const logError2 = /* @__PURE__ */ ref("");
    const logBox = /* @__PURE__ */ ref(null);
    let logEs = null;
    const LEVEL_LABELS = { INFO: "信息", WARNING: "警告", ERROR: "错误", CRITICAL: "严重", DEBUG: "调试" };
    const levelLabel = (lv) => LEVEL_LABELS[lv] || lv;
    function logLevelClass(level) {
      if (level === "ERROR" || level === "CRITICAL") return "log-err";
      if (level === "WARNING") return "log-warn";
      return "";
    }
    const LOG_TRANSLATIONS = [
      [/Application startup complete\./, "应用启动完成"],
      [/Uvicorn running on (http:\/\/[^ )]+)/, "服务已启动并监听 $1"],
      [/Waiting for application startup\./, "等待应用启动..."],
      [/Shutting down/, "正在关闭服务"],
      [/Invalid HTTP request received\./, "收到无效 HTTP 请求(通常是端口扫描探测, 可忽略)"],
      [/Connection lost/, "客户端连接断开"],
      [/Started server process \[\d+\]/, "服务进程已启动"],
      [/Finished server process \[\d+\]/, "服务进程已结束"],
      [/Press CTRL\+C to quit/, "按 Ctrl+C 退出"]
    ];
    function translateMsg(msg) {
      let out = msg || "";
      for (const [re, rep] of LOG_TRANSLATIONS) out = out.replace(re, rep);
      const m = out.match(/(\d+\.\d+\.\d+\.\d+:\d+) - "(\w+) (\S+) HTTP\/1\.1" (\d+)/);
      if (m) {
        let path = m[3];
        if (path.startsWith("/api/logs/stream")) path = "/api/logs/stream (实时日志流)";
        out = out.replace(m[0], `${m[1]} - 请求: ${m[2]} ${path} → ${m[4]}`);
      }
      out = out.replace(/(token=)[A-Za-z0-9_.\-]{20,}/g, "$1[已隐藏]");
      return out;
    }
    function scrollTop() {
      if (logAutoScroll.value) {
        nextTick(() => {
          if (logBox.value) logBox.value.scrollTop = 0;
        });
      }
    }
    async function openStream() {
      logError2.value = "";
      try {
        const data = await http.get("/api/logs?tail=200");
        logLines.value = (data.lines || []).reverse();
      } catch (e) {
        logError2.value = `历史日志加载失败: ${e.message}`;
      }
      const token = localStorage.getItem("strmhub_token") || "";
      if (logEs) logEs.close();
      logEs = new EventSource(`/api/logs/stream?token=${encodeURIComponent(token)}`);
      logEs.onmessage = (e) => {
        if (logPaused.value) return;
        try {
          const ln = JSON.parse(e.data);
          if (ln.error) {
            logError2.value = `SSE 连接失败: ${ln.error}`;
            return;
          }
          logLines.value.unshift(ln);
          if (logLines.value.length > 1e3) logLines.value.length = 1e3;
          scrollTop();
        } catch {
        }
      };
      logEs.onerror = () => {
        logError2.value = "SSE 连接断开, 自动重连中...";
      };
    }
    function logClear() {
      logLines.value = [];
    }
    onMounted(openStream);
    onUnmounted(() => {
      if (logEs) logEs.close();
    });
    return (_ctx, _cache) => {
      return openBlock(), createElementBlock(Fragment, null, [
        _cache[3] || (_cache[3] = createBaseVNode("h1", null, "实时日志", -1)),
        createBaseVNode("div", _hoisted_1$2, [
          createBaseVNode("div", _hoisted_2$2, [
            _cache[2] || (_cache[2] = createBaseVNode("span", { class: "log-title" }, "实时日志", -1)),
            createBaseVNode("button", {
              class: normalizeClass(["log-toggle", { on: !logPaused.value }]),
              onClick: _cache[0] || (_cache[0] = ($event) => logPaused.value = !logPaused.value)
            }, toDisplayString(logPaused.value ? "已暂停" : "实时"), 3),
            createBaseVNode("button", {
              class: normalizeClass(["log-toggle", { on: logAutoScroll.value }]),
              onClick: _cache[1] || (_cache[1] = ($event) => logAutoScroll.value = !logAutoScroll.value)
            }, " 自动滚动 " + toDisplayString(logAutoScroll.value ? "开" : "关"), 3),
            createBaseVNode("button", {
              class: "log-btn",
              onClick: logClear
            }, "清空")
          ]),
          logError2.value ? (openBlock(), createElementBlock("div", _hoisted_3$2, toDisplayString(logError2.value), 1)) : createCommentVNode("", true),
          createBaseVNode("div", {
            ref_key: "logBox",
            ref: logBox,
            class: "log-body"
          }, [
            (openBlock(true), createElementBlock(Fragment, null, renderList(logLines.value, (ln, i) => {
              return openBlock(), createElementBlock("div", {
                key: i,
                class: normalizeClass(["log-line", logLevelClass(ln.level)])
              }, [
                createBaseVNode("span", _hoisted_4$2, toDisplayString(new Date(ln.ts * 1e3).toLocaleTimeString()), 1),
                createBaseVNode("span", _hoisted_5$2, "[" + toDisplayString(levelLabel(ln.level)) + "]", 1),
                createBaseVNode("span", _hoisted_6$2, toDisplayString(translateMsg(ln.msg)), 1)
              ], 2);
            }), 128)),
            !logLines.value.length ? (openBlock(), createElementBlock("div", _hoisted_7$2, "暂无日志...")) : createCommentVNode("", true)
          ], 512)
        ])
      ], 64);
    };
  }
};
const _hoisted_1$1 = { class: "card" };
const _hoisted_2$1 = { class: "grid2" };
const _hoisted_3$1 = { class: "span2" };
const _hoisted_4$1 = {
  class: "row",
  style: { "margin-top": "10px" }
};
const _hoisted_5$1 = { class: "card" };
const _hoisted_6$1 = { class: "muted" };
const _hoisted_7$1 = ["onClick"];
const _hoisted_8$1 = { key: 0 };
const _sfc_main$1 = {
  __name: "Automation",
  setup(__props) {
    const rules = /* @__PURE__ */ ref([]);
    const form = /* @__PURE__ */ ref({ name: "", trigger: "webhook", action_chain: "", token: "" });
    const msg = /* @__PURE__ */ ref("");
    async function load() {
      rules.value = await automationApi.list();
    }
    onMounted(load);
    async function create() {
      msg.value = "";
      try {
        const chain = form.value.action_chain.split("\n").map((s) => s.trim()).filter(Boolean);
        await automationApi.create({
          name: form.value.name,
          trigger: form.value.trigger,
          action_chain: chain,
          token: form.value.token
        });
        form.value = { name: "", trigger: "webhook", action_chain: "", token: "" };
        await load();
        msg.value = { type: "ok", text: "规则已创建" };
      } catch (e) {
        msg.value = { type: "err", text: e.message };
      }
    }
    async function remove2(id) {
      if (!confirm("确认删除规则?")) return;
      await automationApi.remove(id);
      await load();
    }
    return (_ctx, _cache) => {
      return openBlock(), createElementBlock(Fragment, null, [
        _cache[13] || (_cache[13] = createBaseVNode("h1", null, "Webhook 联动", -1)),
        createBaseVNode("div", _hoisted_1$1, [
          _cache[9] || (_cache[9] = createBaseVNode("h2", null, "新建规则", -1)),
          createBaseVNode("div", _hoisted_2$1, [
            createBaseVNode("div", null, [
              _cache[4] || (_cache[4] = createBaseVNode("label", null, "规则名称", -1)),
              withDirectives(createBaseVNode("input", {
                "onUpdate:modelValue": _cache[0] || (_cache[0] = ($event) => form.value.name = $event),
                placeholder: "如: 转存即触发"
              }, null, 512), [
                [vModelText, form.value.name]
              ])
            ]),
            createBaseVNode("div", null, [
              _cache[6] || (_cache[6] = createBaseVNode("label", null, "触发类型", -1)),
              withDirectives(createBaseVNode("select", {
                "onUpdate:modelValue": _cache[1] || (_cache[1] = ($event) => form.value.trigger = $event)
              }, [..._cache[5] || (_cache[5] = [
                createBaseVNode("option", { value: "webhook" }, "webhook(通用)", -1),
                createBaseVNode("option", { value: "qas_strm" }, "qas_strm(Quark-Auto-Save)", -1),
                createBaseVNode("option", { value: "cs_strm" }, "cs_strm(CloudSaver)", -1)
              ])], 512), [
                [vModelSelect, form.value.trigger]
              ])
            ]),
            createBaseVNode("div", _hoisted_3$1, [
              _cache[7] || (_cache[7] = createBaseVNode("label", null, "动作链(每行一个: strm_scan:任务ID / scrape:目录 / emby_refresh)", -1)),
              withDirectives(createBaseVNode("textarea", {
                "onUpdate:modelValue": _cache[2] || (_cache[2] = ($event) => form.value.action_chain = $event),
                rows: "3",
                placeholder: "strm_scan:1\nscrape:/strm/media\nemby_refresh"
              }, null, 512), [
                [vModelText, form.value.action_chain]
              ])
            ]),
            createBaseVNode("div", null, [
              _cache[8] || (_cache[8] = createBaseVNode("label", null, "token(留空自动生成)", -1)),
              withDirectives(createBaseVNode("input", {
                "onUpdate:modelValue": _cache[3] || (_cache[3] = ($event) => form.value.token = $event)
              }, null, 512), [
                [vModelText, form.value.token]
              ])
            ])
          ]),
          createBaseVNode("div", _hoisted_4$1, [
            createBaseVNode("button", {
              class: "primary",
              onClick: create
            }, "创建"),
            msg.value ? (openBlock(), createElementBlock("div", {
              key: 0,
              class: normalizeClass(["msg", msg.value.type])
            }, toDisplayString(msg.value.text), 3)) : createCommentVNode("", true)
          ])
        ]),
        createBaseVNode("div", _hoisted_5$1, [
          _cache[12] || (_cache[12] = createBaseVNode("h2", null, "规则列表", -1)),
          createBaseVNode("table", null, [
            _cache[11] || (_cache[11] = createBaseVNode("tr", null, [
              createBaseVNode("th", null, "ID"),
              createBaseVNode("th", null, "名称"),
              createBaseVNode("th", null, "触发"),
              createBaseVNode("th", null, "动作链"),
              createBaseVNode("th", null, "触发地址"),
              createBaseVNode("th", null, "操作")
            ], -1)),
            (openBlock(true), createElementBlock(Fragment, null, renderList(rules.value, (r) => {
              return openBlock(), createElementBlock("tr", {
                key: r.id
              }, [
                createBaseVNode("td", null, toDisplayString(r.id), 1),
                createBaseVNode("td", null, toDisplayString(r.name), 1),
                createBaseVNode("td", null, toDisplayString(r.trigger), 1),
                createBaseVNode("td", null, [
                  (openBlock(true), createElementBlock(Fragment, null, renderList(r.action_chain, (a) => {
                    return openBlock(), createElementBlock("code", {
                      key: a,
                      style: { "display": "block" }
                    }, toDisplayString(a), 1);
                  }), 128))
                ]),
                createBaseVNode("td", _hoisted_6$1, [
                  createBaseVNode("code", null, "/api/automation/webhook/" + toDisplayString(r.token), 1)
                ]),
                createBaseVNode("td", null, [
                  createBaseVNode("button", {
                    class: "danger",
                    onClick: ($event) => remove2(r.id)
                  }, "删除", 8, _hoisted_7$1)
                ])
              ]);
            }), 128)),
            !rules.value.length ? (openBlock(), createElementBlock("tr", _hoisted_8$1, [..._cache[10] || (_cache[10] = [
              createBaseVNode("td", {
                colspan: "6",
                class: "muted"
              }, "暂无规则", -1)
            ])])) : createCommentVNode("", true)
          ])
        ])
      ], 64);
    };
  }
};
const Automation = /* @__PURE__ */ _export_sfc(_sfc_main$1, [["__scopeId", "data-v-8950db96"]]);
const _hoisted_1 = { key: 0 };
const _hoisted_2 = {
  key: 1,
  class: "layout"
};
const _hoisted_3 = { class: "side" };
const _hoisted_4 = ["onClick"];
const _hoisted_5 = {
  key: 0,
  class: "nav-error"
};
const _hoisted_6 = ["onClick"];
const _hoisted_7 = { class: "side-foot" };
const _hoisted_8 = { class: "muted" };
const _hoisted_9 = { class: "main" };
const _sfc_main = {
  __name: "App",
  setup(__props) {
    const view = /* @__PURE__ */ ref(localStorage.getItem("strmhub_view") || "dashboard");
    const driverFilter = /* @__PURE__ */ ref(localStorage.getItem("strmhub_driver") || "");
    const authed = /* @__PURE__ */ ref(isAuthed());
    const health = /* @__PURE__ */ ref("...");
    const drivers = /* @__PURE__ */ ref(JSON.parse(localStorage.getItem("strmhub_drivers") || "[]"));
    const driversError = /* @__PURE__ */ ref("");
    const netpanOpen = /* @__PURE__ */ ref(localStorage.getItem("strmhub_netpan_open") !== "0");
    const baseViews = [
      { id: "dashboard", label: "总览", comp: _sfc_main$7 },
      { id: "tasks", label: "STRM 任务", comp: _sfc_main$5 },
      { id: "scrape", label: "刮削与海报墙", comp: _sfc_main$4 },
      { id: "organize", label: "目录整理", comp: _sfc_main$3 },
      { id: "automation", label: "Webhook 联动", comp: Automation },
      { id: "logs", label: "实时日志", comp: _sfc_main$2 }
      // 日志页(不进菜单, 由右上角按钮进入)
    ];
    const menuViews = computed(() => baseViews.filter((v) => v.id !== "logs"));
    const accountViews = computed(() => drivers.value.map((d) => ({
      id: `accounts:${d.name}`,
      label: `${d.label}管理`,
      comp: _sfc_main$6,
      driver: d.name
    })));
    const fallbackAccountView = {
      id: "accounts:all",
      label: "全部账户",
      comp: _sfc_main$6,
      driver: ""
    };
    const current = computed(() => {
      if (view.value.startsWith("accounts:")) {
        const found = accountViews.value.find((v) => v.id === view.value);
        if (found) return found;
        if (view.value === "accounts:all") return fallbackAccountView;
        return { ...fallbackAccountView, driver: driverFilter.value };
      }
      return baseViews.find((v) => v.id === view.value) || baseViews[0];
    });
    async function loadDrivers() {
      if (!authed.value) return;
      driversError.value = "";
      try {
        drivers.value = await accountApi.drivers();
        localStorage.setItem("strmhub_drivers", JSON.stringify(drivers.value));
        if (!drivers.value.length) {
          driversError.value = "驱动列表为空";
        }
      } catch (e) {
        if (!drivers.value.length) {
          driversError.value = `驱动列表加载失败: ${e.message}`;
        }
        console.error("[STRMhub]", `驱动列表刷新失败: ${e.message}`);
      }
    }
    function switchView(id) {
      view.value = id;
      if (id.startsWith("accounts:")) {
        driverFilter.value = id.split(":")[1];
        localStorage.setItem("strmhub_driver", driverFilter.value);
      }
      localStorage.setItem("strmhub_view", id);
    }
    function toggleNetpan() {
      netpanOpen.value = !netpanOpen.value;
      localStorage.setItem("strmhub_netpan_open", netpanOpen.value ? "1" : "0");
    }
    async function logout() {
      setToken("");
      authed.value = false;
      drivers.value = [];
      view.value = "dashboard";
    }
    watch(authed, (v) => {
      if (v) loadDrivers();
    });
    onMounted(async () => {
      window.addEventListener("strmhub-unauthorized", () => {
        authed.value = false;
      });
      try {
        const h = await fetch("/api/health").then((r) => r.json());
        health.value = h.status;
      } catch {
        health.value = "offline";
      }
      if (authed.value) loadDrivers();
    });
    return (_ctx, _cache) => {
      return !authed.value ? (openBlock(), createElementBlock("div", _hoisted_1, [
        createVNode(Login, {
          onLogin: _cache[0] || (_cache[0] = ($event) => authed.value = true)
        })
      ])) : (openBlock(), createElementBlock("div", _hoisted_2, [
        createBaseVNode("aside", _hoisted_3, [
          _cache[4] || (_cache[4] = createBaseVNode("div", { class: "logo" }, "STRMhub", -1)),
          createBaseVNode("nav", null, [
            (openBlock(true), createElementBlock(Fragment, null, renderList(menuViews.value, (v) => {
              return openBlock(), createElementBlock("a", {
                key: v.id,
                class: normalizeClass({ active: view.value === v.id }),
                href: "#",
                onClick: withModifiers(($event) => switchView(v.id), ["prevent"])
              }, toDisplayString(v.label), 11, _hoisted_4);
            }), 128)),
            accountViews.value.length || driversError.value ? (openBlock(), createElementBlock("div", {
              key: 0,
              class: normalizeClass(["nav-group nav-toggle", { open: netpanOpen.value }]),
              onClick: toggleNetpan
            }, [..._cache[3] || (_cache[3] = [
              createBaseVNode("span", { class: "arrow" }, null, -1),
              createTextVNode(" 网盘管理 ", -1)
            ])], 2)) : createCommentVNode("", true),
            netpanOpen.value ? (openBlock(), createElementBlock(Fragment, { key: 1 }, [
              driversError.value ? (openBlock(), createElementBlock("div", _hoisted_5, toDisplayString(driversError.value), 1)) : createCommentVNode("", true),
              (openBlock(true), createElementBlock(Fragment, null, renderList(accountViews.value, (v) => {
                return openBlock(), createElementBlock("a", {
                  key: v.id,
                  class: normalizeClass(["nav-sub", { active: view.value === v.id }]),
                  href: "#",
                  onClick: withModifiers(($event) => switchView(v.id), ["prevent"])
                }, toDisplayString(v.label), 11, _hoisted_6);
              }), 128)),
              driversError.value ? (openBlock(), createElementBlock("a", {
                key: 1,
                class: normalizeClass(["nav-sub", { active: view.value === "accounts:all" }]),
                href: "#",
                onClick: _cache[1] || (_cache[1] = withModifiers(($event) => switchView("accounts:all"), ["prevent"]))
              }, toDisplayString(fallbackAccountView.label), 3)) : createCommentVNode("", true)
            ], 64)) : createCommentVNode("", true)
          ]),
          createBaseVNode("div", _hoisted_7, [
            createBaseVNode("span", _hoisted_8, "后端: " + toDisplayString(health.value), 1),
            createBaseVNode("button", { onClick: logout }, "退出")
          ])
        ]),
        createBaseVNode("main", _hoisted_9, [
          (openBlock(), createBlock(resolveDynamicComponent(current.value.comp), {
            "driver-type": current.value.driver || ""
          }, null, 8, ["driver-type"]))
        ]),
        createBaseVNode("button", {
          class: normalizeClass(["log-fab", { on: view.value === "logs" }]),
          title: "实时日志",
          onClick: _cache[2] || (_cache[2] = ($event) => switchView("logs"))
        }, "📄 实时日志", 2)
      ]));
    };
  }
};
const App = /* @__PURE__ */ _export_sfc(_sfc_main, [["__scopeId", "data-v-f96b848e"]]);
createApp(App).mount("#app");
