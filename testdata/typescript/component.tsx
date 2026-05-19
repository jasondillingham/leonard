// TSX fixture for Leonard's TypeScript parser. Covers JSX expressions
// embedded inside function bodies and arrow-function components.

import * as React from "react";

export interface Props {
    name: string;
    initial?: number;
}

export function Greeting({ name }: Props) {
    return <div className="hello">Hello, {name}!</div>;
}

export const Counter = ({ initial }: { initial: number }) => {
    const [count, setCount] = React.useState(initial);
    return (
        <button onClick={() => setCount(count + 1)}>
            {count}
        </button>
    );
};

export class Panel extends React.Component<Props> {
    render() {
        return <section>{this.props.name}</section>;
    }
}
