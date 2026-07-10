import { Heading } from '../components/ui/heading';
import { Text } from '../components/ui/text';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '../components/ui/table';
import { Badge } from '../components/ui/badge';
import { Button } from '../components/ui/button';

export default function Rules() {
  return (
    <div className="p-8">
      <div className="flex items-center justify-between">
        <Heading>Rules</Heading>
        <Button color="indigo">Dry-run</Button>
      </div>
      <Text className="mt-2">M0 skeleton — real CRUD + dry-run + auto-rollback ships in M1.</Text>
      <div className="mt-6">
        <Table>
          <TableHead>
            <TableRow>
              <TableHeader>Kind</TableHeader>
              <TableHeader>Pattern</TableHeader>
              <TableHeader>Action</TableHeader>
              <TableHeader>Status</TableHeader>
            </TableRow>
          </TableHead>
          <TableBody>
            <TableRow>
              <TableCell>DOMAIN-SUFFIX</TableCell>
              <TableCell>example.com</TableCell>
              <TableCell>direct</TableCell>
              <TableCell><Badge color="lime">enabled</Badge></TableCell>
            </TableRow>
            <TableRow>
              <TableCell>GEOSITE</TableCell>
              <TableCell>cn</TableCell>
              <TableCell>direct</TableCell>
              <TableCell><Badge color="lime">enabled</Badge></TableCell>
            </TableRow>
            <TableRow>
              <TableCell>MATCH</TableCell>
              <TableCell>*</TableCell>
              <TableCell>wg1</TableCell>
              <TableCell><Badge color="zinc">draft</Badge></TableCell>
            </TableRow>
          </TableBody>
        </Table>
      </div>
    </div>
  );
}
