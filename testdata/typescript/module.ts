// Top-level TypeScript fixture for Leonard's TypeScript parser.
//
// Exercises module-level functions, classes, methods, interfaces, type
// aliases, and const/let/var declarations. The companion file
// `component.tsx` covers TSX-specific syntax.

import { something } from "./other";

export const VERSION = "0.1";
const PUBLIC_NAME = "leonard";
let counter = 0;
var legacyFlag: boolean = true;

const a = 1, b = 2;

export function hello(name: string): string {
    return "hi " + name;
}

function _helper() {
    return 42;
}

export async function fetchAll(urls: string[], opts?: { signal?: AbortSignal }): Promise<number> {
    return urls.length;
}

export interface Greeter {
    greet(name: string): string;
}

export type ID = string | number;

type Pair<K, V> = { key: K; value: V };

export class Container<T> {
    static created = 0;
    private items: T[] = [];

    constructor(initial: T[]) {
        this.items = initial;
    }

    public add(item: T): void {
        this.items.push(item);
    }

    private _refresh(): number {
        return this.items.length;
    }

    async load(url: string): Promise<T[]> {
        return [];
    }
}

class _PrivateBox {
    constructor() {}
}
